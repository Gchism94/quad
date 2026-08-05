// SPDX-License-Identifier: Apache-2.0

// Package gitlab implements adapter.Adapter for GitLab (gitlab.com or
// self-hosted) against the /api/v4 REST API.
//
// GitLab differs from GitHub and the Gitea family in ways this adapter handles:
//   - Auth uses a personal access token in the PRIVATE-TOKEN header.
//   - Namespaces are groups (POST /groups), not orgs.
//   - There is no "generate from template"; CreateRepoFromTemplate forks the
//     template project into the group, then breaks the fork relationship so the
//     student repo is independent. The fork/import may complete asynchronously.
//   - Adding a member needs a numeric user id and a numeric access level.
//   - Project lookups use the URL-encoded "namespace%2Fpath" as the id.
//   - Locking protects the default branch (push_access_level=0) rather than
//     archiving, so the repo stays visible.
//
// PRIVACY: no method stores a student's legal name, SIS id, or plaintext email.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

// Config holds credentials for a GitLab instance.
type Config struct {
	BaseURL string // instance root, e.g. https://gitlab.com
	Token   string // personal access token with the "api" scope
}

// Adapter implements adapter.Adapter for GitLab.
type Adapter struct {
	httpc   *http.Client
	baseURL string // instance root (no /api/v4 suffix); do() appends the API path
}

// Compile-time guarantee that *Adapter stays in sync with the interface.
var _ adapter.Adapter = (*Adapter)(nil)

// authTransport injects the PRIVATE-TOKEN header into every request. GitLab
// personal access tokens do not expire unless configured to.
type authTransport struct {
	base  http.RoundTripper
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("PRIVATE-TOKEN", t.token)
	if r.Header.Get("Accept") == "" {
		r.Header.Set("Accept", "application/json")
	}
	return t.base.RoundTrip(r)
}

// New constructs a GitLab adapter. BaseURL is the instance root (default
// https://gitlab.com); Token is a personal access token with the "api" scope
// (group creation, project fork, member and webhook management).
func New(cfg Config) (*Adapter, error) {
	if cfg.Token == "" {
		return nil, errors.New("gitlab: Token required")
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://gitlab.com"
	}
	return &Adapter{
		httpc: &http.Client{
			Transport: &authTransport{base: http.DefaultTransport, token: cfg.Token},
			Timeout:   30 * time.Second,
		},
		baseURL: base,
	}, nil
}

// Host returns the host this adapter targets.
func (a *Adapter) Host() adapter.Host { return adapter.HostGitLab }

// RepoWebURL returns the browser URL for a project.
func (a *Adapter) RepoWebURL(repo adapter.RepoRef) string {
	return strings.TrimRight(a.baseURL, "/") + "/" + repo.Namespace + "/" + repo.Name
}

// --- HTTP plumbing (mirrors pkg/adapter/forgejo) --------------------------

type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("gitlab api: status %d: %s", e.Status, e.Body)
}

// statusOf extracts the HTTP status from an *apiError, or 0 for other errors.
func statusOf(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// do performs an /api/v4 request and, when the response status is in ok, decodes
// the body into out (when non-nil). Any other status yields an *apiError.
func (a *Adapter) do(ctx context.Context, method, path string, in, out any, ok ...int) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+"/api/v4"+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	for _, c := range ok {
		if resp.StatusCode == c {
			if out != nil && len(raw) > 0 {
				return json.Unmarshal(raw, out)
			}
			return nil
		}
	}
	return &apiError{Status: resp.StatusCode, Body: string(raw)}
}

// projectID is the URL-encoded "namespace/name" GitLab accepts as a project id.
func projectID(repo adapter.RepoRef) string {
	return url.PathEscape(repo.Namespace + "/" + repo.Name)
}

// --- adapter.Adapter methods ----------------------------------------------

// EnsureNamespace makes sure the GitLab group exists, creating it if necessary.
// Idempotent: on a create conflict, it re-reads the group by path and returns it.
func (a *Adapter) EnsureNamespace(ctx context.Context, slug string) (adapter.NamespaceRef, error) {
	ref := adapter.NamespaceRef{Host: adapter.HostGitLab, Slug: slug}
	// Fast path: the group already exists (lookup by URL-encoded full path).
	if err := a.do(ctx, http.MethodGet, "/groups/"+url.PathEscape(slug), nil, nil, http.StatusOK); err == nil {
		return ref, nil
	} else if statusOf(err) != http.StatusNotFound {
		return adapter.NamespaceRef{}, err
	}
	// Create it.
	in := map[string]string{"name": slug, "path": slug}
	createErr := a.do(ctx, http.MethodPost, "/groups", in, nil, http.StatusCreated)
	if createErr != nil {
		// 400/409 can mean "already taken" — confirm with a re-GET rather than
		// inferring success from the status alone.
		s := statusOf(createErr)
		if s == http.StatusConflict || s == http.StatusBadRequest {
			if getErr := a.do(ctx, http.MethodGet, "/groups/"+url.PathEscape(slug), nil, nil, http.StatusOK); getErr == nil {
				return ref, nil
			}
		}
		return adapter.NamespaceRef{}, createErr
	}
	return ref, nil
}

// CreateRepoFromTemplate creates a project by forking the template project into
// the target group, then breaking the fork relationship so the student repo is
// independent. The fork (import) may complete asynchronously — the project exists
// immediately even if its content arrives shortly after. The call is idempotent:
// if the target project already exists it is returned as-is.
//
// An alternative to forking is creating an empty project with import_url set to
// the template's clone URL; forking is used here because it needs no second
// credential and copies repository content directly.
func (a *Adapter) CreateRepoFromTemplate(ctx context.Context, tmpl adapter.TemplateRef, ns adapter.NamespaceRef, name string, opts adapter.CreateRepoOptions) (adapter.RepoRef, error) {
	ref := adapter.RepoRef{Host: adapter.HostGitLab, Namespace: ns.Slug, Name: name}
	// Idempotent: return the ref if the project already exists.
	exists, err := a.RepoExists(ctx, ref)
	if err != nil {
		return adapter.RepoRef{}, err
	}
	if exists {
		return ref, nil
	}

	visibility := "public"
	if opts.Private {
		visibility = "private"
	}
	in := map[string]any{
		"namespace_path": ns.Slug,
		"name":           name,
		"path":           name,
		"visibility":     visibility,
	}
	templateID := url.PathEscape(tmpl.Namespace + "/" + tmpl.Name)
	var forked struct {
		ID int64 `json:"id"`
	}
	if createErr := a.do(ctx, http.MethodPost, "/projects/"+templateID+"/fork", in, &forked, http.StatusCreated); createErr != nil {
		// A conflict may mean the project already exists (concurrent provision) —
		// confirm via RepoExists rather than trusting the status code.
		if ok, _ := a.RepoExists(ctx, ref); ok {
			return ref, nil
		}
		return adapter.RepoRef{}, createErr
	}

	// Break the fork relationship so the student project is independent. The fork
	// link removal is best-effort: a transient failure here must not orphan a
	// successfully-created project, and the call is safe to retry on re-provision.
	_ = a.do(ctx, http.MethodDelete, "/projects/"+projectID(ref)+"/fork", nil, nil, http.StatusNoContent, http.StatusNotFound)
	return ref, nil
}

// RepoExists reports whether the project exists.
func (a *Adapter) RepoExists(ctx context.Context, repo adapter.RepoRef) (bool, error) {
	err := a.do(ctx, http.MethodGet, "/projects/"+projectID(repo), nil, nil, http.StatusOK)
	if err == nil {
		return true, nil
	}
	if statusOf(err) == http.StatusNotFound {
		return false, nil
	}
	return false, err
}

// accessLevel maps a normalized Role to a GitLab numeric access level.
func accessLevel(role adapter.Role) int {
	switch role {
	case adapter.RoleRead:
		return 20 // Reporter
	case adapter.RoleAdmin:
		return 40 // Maintainer
	default:
		return 30 // Developer (push)
	}
}

// resolveUserID looks up a GitLab numeric user id by username.
func (a *Adapter) resolveUserID(ctx context.Context, username string) (int64, error) {
	var users []struct {
		ID int64 `json:"id"`
	}
	if err := a.do(ctx, http.MethodGet, "/users?username="+url.QueryEscape(username), nil, &users, http.StatusOK); err != nil {
		return 0, err
	}
	if len(users) == 0 {
		return 0, fmt.Errorf("gitlab: no user with username %q (they must have an account on this instance)", username)
	}
	return users[0].ID, nil
}

// memberAlreadySatisfied reports whether a failed member-add response means the
// user already has equal-or-higher access, so the add is a harmless no-op:
//   - 409: the user is already a direct member (idempotent).
//   - 400: GitLab rejects adding a member below an access level they already hold
//     via inherited group membership (e.g. an Owner/Maintainer of the group).
func memberAlreadySatisfied(status int, body []byte) bool {
	if status == http.StatusConflict {
		return true
	}
	if status == http.StatusBadRequest {
		b := strings.ToLower(string(body))
		return strings.Contains(b, "inherited membership") ||
			strings.Contains(b, "greater than or equal to")
	}
	return false
}

// SetCollaborator grants username the given role on repo. A roster user with no
// prior access is added at the requested level. The call is idempotent and
// tolerant of users who already have access: if they are already a direct member
// (409) or already hold an equal-or-higher level via inherited group membership
// (400), the redundant add is treated as success rather than failing provisioning.
func (a *Adapter) SetCollaborator(ctx context.Context, repo adapter.RepoRef, username string, role adapter.Role) error {
	uid, err := a.resolveUserID(ctx, username)
	if err != nil {
		return err
	}
	in := map[string]any{"user_id": uid, "access_level": accessLevel(role)}
	pid := projectID(repo)
	err = a.do(ctx, http.MethodPost, "/projects/"+pid+"/members", in, nil, http.StatusOK, http.StatusCreated)
	if err == nil {
		return nil
	}
	// Inspect the response: an already-satisfied membership is non-fatal because
	// the user already has the access we were trying to grant.
	var ae *apiError
	if errors.As(err, &ae) && memberAlreadySatisfied(ae.Status, []byte(ae.Body)) {
		return nil
	}
	return err
}

// RemoveCollaborator revokes username's access to repo.
func (a *Adapter) RemoveCollaborator(ctx context.Context, repo adapter.RepoRef, username string) error {
	uid, err := a.resolveUserID(ctx, username)
	if err != nil {
		return err
	}
	path := "/projects/" + projectID(repo) + "/members/" + strconv.FormatInt(uid, 10)
	return a.do(ctx, http.MethodDelete, path, nil, nil, http.StatusNoContent)
}

// LatestCommit returns the most recent commit on a branch (empty = default
// branch). An empty repository yields a zero Commit and a nil error.
func (a *Adapter) LatestCommit(ctx context.Context, repo adapter.RepoRef, branch string) (adapter.Commit, error) {
	path := "/projects/" + projectID(repo) + "/repository/commits?per_page=1"
	if branch != "" {
		path += "&ref_name=" + url.QueryEscape(branch)
	}
	var commits []struct {
		ID          string    `json:"id"`
		Message     string    `json:"message"`
		CommittedAt time.Time `json:"committed_date"`
		AuthorName  string    `json:"author_name"`
	}
	if err := a.do(ctx, http.MethodGet, path, nil, &commits, http.StatusOK); err != nil {
		// 404 here means the repository is empty (no commits yet).
		if statusOf(err) == http.StatusNotFound {
			return adapter.Commit{}, nil
		}
		return adapter.Commit{}, err
	}
	if len(commits) == 0 {
		return adapter.Commit{}, nil
	}
	c := commits[0]
	// AuthorUsername is intentionally left empty: a GitLab commit exposes the
	// author's display name, not their GitLab username, and we never store names.
	return adapter.Commit{SHA: c.ID, Message: c.Message, Timestamp: c.CommittedAt}, nil
}

// LockRepo protects the default branch so no one may push, enforcing a deadline
// while keeping the repo visible. Reversible via UnlockRepo.
func (a *Adapter) LockRepo(ctx context.Context, repo adapter.RepoRef) error {
	return a.setBranchProtection(ctx, repo, 0) // push_access_level 0 = no one
}

// UnlockRepo restores developer push by recreating the protection at the
// developer access level.
func (a *Adapter) UnlockRepo(ctx context.Context, repo adapter.RepoRef) error {
	return a.setBranchProtection(ctx, repo, 30) // 30 = Developer
}

// setBranchProtection deletes any existing protection on the default branch and
// recreates it with the given push access level, so lock/unlock are deterministic
// and idempotent.
func (a *Adapter) setBranchProtection(ctx context.Context, repo adapter.RepoRef, pushLevel int) error {
	branch, err := a.defaultBranch(ctx, repo)
	if err != nil {
		return err
	}
	pid := projectID(repo)
	// Remove any existing protection first (ignore 404), then recreate.
	_ = a.do(ctx, http.MethodDelete, "/projects/"+pid+"/protected_branches/"+url.PathEscape(branch),
		nil, nil, http.StatusNoContent, http.StatusOK, http.StatusNotFound)
	in := map[string]any{
		"name":               branch,
		"push_access_level":  pushLevel,
		"merge_access_level": pushLevel,
	}
	return a.do(ctx, http.MethodPost, "/projects/"+pid+"/protected_branches", in, nil, http.StatusCreated, http.StatusOK)
}

// defaultBranch returns the project's default branch, falling back to "main".
func (a *Adapter) defaultBranch(ctx context.Context, repo adapter.RepoRef) (string, error) {
	var proj struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := a.do(ctx, http.MethodGet, "/projects/"+projectID(repo), nil, &proj, http.StatusOK); err != nil {
		return "", err
	}
	if proj.DefaultBranch == "" {
		return "main", nil
	}
	return proj.DefaultBranch, nil
}

// EnsureWebhook makes sure a push webhook for spec.URL exists on repo. GitLab
// stores spec.Secret as the hook token and sends it back verbatim in the
// X-Gitlab-Token header (not an HMAC). Idempotent: an existing matching hook is
// updated in place.
func (a *Adapter) EnsureWebhook(ctx context.Context, repo adapter.RepoRef, spec adapter.WebhookSpec) error {
	pid := projectID(repo)
	var hooks []struct {
		ID  int64  `json:"id"`
		URL string `json:"url"`
	}
	if err := a.do(ctx, http.MethodGet, "/projects/"+pid+"/hooks", nil, &hooks, http.StatusOK); err != nil {
		return err
	}
	in := map[string]any{
		"url":         spec.URL,
		"push_events": true,
		"token":       spec.Secret,
	}
	for _, h := range hooks {
		if h.URL == spec.URL {
			// Update in place (refreshes the token).
			return a.do(ctx, http.MethodPut, "/projects/"+pid+"/hooks/"+strconv.FormatInt(h.ID, 10), in, nil, http.StatusOK)
		}
	}
	return a.do(ctx, http.MethodPost, "/projects/"+pid+"/hooks", in, nil, http.StatusCreated)
}

// DispatchGrading returns ErrNotImplemented: Cairn grades on its own sandboxed
// runners rather than dispatching GitLab CI.
func (a *Adapter) DispatchGrading(_ context.Context, _ adapter.GradingDispatch) error {
	return adapter.ErrNotImplemented
}

// GradingResult reads the commit's CI statuses and maps the latest to a
// CheckResult. Numeric scores come from Cairn's runner path, not here.
func (a *Adapter) GradingResult(ctx context.Context, repo adapter.RepoRef, sha string) (adapter.CheckResult, error) {
	path := "/projects/" + projectID(repo) + "/repository/commits/" + url.PathEscape(sha) + "/statuses"
	var statuses []struct {
		Status    string `json:"status"`
		TargetURL string `json:"target_url"`
	}
	if err := a.do(ctx, http.MethodGet, path, nil, &statuses, http.StatusOK); err != nil {
		return adapter.CheckResult{}, err
	}
	if len(statuses) == 0 {
		return adapter.CheckResult{Status: adapter.CheckPending}, nil
	}
	latest := statuses[0]
	out := adapter.CheckResult{DetailURL: latest.TargetURL}
	switch latest.Status {
	case "success":
		out.Status = adapter.CheckPassed
	case "failed":
		out.Status = adapter.CheckFailed
	case "canceled", "skipped":
		out.Status = adapter.CheckError
	case "running":
		out.Status = adapter.CheckRunning
	default: // pending, created, manual, ...
		out.Status = adapter.CheckPending
	}
	return out, nil
}
