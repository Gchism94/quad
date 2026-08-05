// SPDX-License-Identifier: Apache-2.0

// Package forgejo implements adapter.Adapter for Forgejo and Gitea instances.
// Forgejo and Gitea share the same /api/v1 REST API, so a single adapter
// targets both, configured by base URL. Authentication uses a static access
// token (no JWT, no refresh) — much simpler than the GitHub App flow.
//
// Limitations:
//   - CreateRepoFromTemplate: Gitea's "generate" API copies only the default
//     branch (git_content). opts.IncludeAllBranches is ignored. The template
//     repository must be marked as a template on the Forgejo/Gitea instance.
//   - GradingResult: uses the combined commit status endpoint; numeric scores
//     come from Cairn's own runner path, not from this endpoint.
package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

// Config holds credentials for a Forgejo or Gitea instance.
type Config struct {
	BaseURL string // instance root, e.g. https://forgejo.example.org
	Token   string // personal or admin access token
}

// Adapter implements adapter.Adapter for Forgejo / Gitea. Forgejo is a hard fork
// of Gitea and the two still share the /api/v1 surface for every endpoint Cairn
// uses, so a single implementation serves both. The target host is a field, not a
// literal, so one binary can register the same instance under both host values
// and stamp returned refs with the right one — letting the impls diverge later
// without an interface change.
type Adapter struct {
	httpc   *http.Client
	baseURL string
	host    adapter.Host
}

// Compile-time guarantee that *Adapter stays in sync with the interface.
var _ adapter.Adapter = (*Adapter)(nil)

// authTransport injects a static Bearer token into every request. No refresh
// is needed — Forgejo/Gitea personal access tokens do not expire by default.
type authTransport struct {
	base  http.RoundTripper
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "token "+t.token)
	if r.Header.Get("Accept") == "" {
		r.Header.Set("Accept", "application/json")
	}
	return t.base.RoundTrip(r)
}

// New constructs a Forgejo/Gitea adapter targeting adapter.HostForgejo. BaseURL is
// the instance root (e.g. https://forgejo.example.org); Token is a personal or
// admin access token with at minimum: organisation creation, repository
// administration, and webhook management permissions.
//
// To target Gitea (or to be explicit about Forgejo), use NewWithHost.
func New(cfg Config) (*Adapter, error) {
	return NewWithHost(cfg, adapter.HostForgejo)
}

// NewWithHost constructs a Gitea-family adapter that reports and stamps the given
// host. host must be adapter.HostForgejo or adapter.HostGitea — they share the
// same /api/v1 surface, differing only in the label carried on returned refs.
func NewWithHost(cfg Config, host adapter.Host) (*Adapter, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("forgejo: BaseURL required")
	}
	if cfg.Token == "" {
		return nil, errors.New("forgejo: Token required")
	}
	if host == "" {
		host = adapter.HostForgejo
	}
	base := strings.TrimRight(cfg.BaseURL, "/") + "/api/v1"
	return &Adapter{
		httpc: &http.Client{
			Transport: &authTransport{base: http.DefaultTransport, token: cfg.Token},
			Timeout:   30 * time.Second,
		},
		baseURL: base,
		host:    host,
	}, nil
}

// Host returns the host this adapter was constructed for (Forgejo or Gitea).
func (a *Adapter) Host() adapter.Host { return a.host }

// RepoWebURL returns the browser URL for a repo. baseURL is the instance root
// plus /api/v1; the web URL is the instance root with that suffix removed.
func (a *Adapter) RepoWebURL(repo adapter.RepoRef) string {
	web := strings.TrimSuffix(a.baseURL, "/api/v1")
	return web + "/" + repo.Namespace + "/" + repo.Name
}

// --- HTTP plumbing (mirrors pkg/adapter/github) ---------------------------

type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("forgejo api: status %d: %s", e.Status, e.Body)
}

// statusOf extracts the HTTP status from an *apiError, or 0 for other errors.
func statusOf(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// do performs a request and, when the response status is in ok, decodes the body
// into out (when non-nil). Any other status yields an *apiError.
func (a *Adapter) do(ctx context.Context, method, path string, in, out any, ok ...int) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, body)
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

// --- adapter.Adapter methods ----------------------------------------------

// EnsureNamespace makes sure the Forgejo/Gitea organisation exists, creating
// it via the API if necessary. Unlike GitHub, Forgejo/Gitea exposes org
// creation via its REST API, so this is fully idempotent.
func (a *Adapter) EnsureNamespace(ctx context.Context, slug string) (adapter.NamespaceRef, error) {
	ref := adapter.NamespaceRef{Host: a.host, Slug: slug}
	err := a.do(ctx, http.MethodGet, "/orgs/"+slug, nil, nil, http.StatusOK)
	if err == nil {
		return ref, nil // org already exists
	}
	if statusOf(err) != http.StatusNotFound {
		return adapter.NamespaceRef{}, err
	}
	// Org not found — create it.
	createErr := a.do(ctx, http.MethodPost, "/orgs", map[string]string{"username": slug}, nil,
		http.StatusCreated)
	if createErr != nil {
		// Gitea returns 409 for an already-existing org and 422 for validation
		// errors — but 422 also fires on genuine failures (invalid slug, etc.).
		// Don't infer "already exists" from the status alone; confirm with a re-GET.
		s := statusOf(createErr)
		if s == http.StatusConflict || s == http.StatusUnprocessableEntity {
			if getErr := a.do(ctx, http.MethodGet, "/orgs/"+slug, nil, nil, http.StatusOK); getErr == nil {
				return ref, nil // org exists (created concurrently or pre-existing)
			}
		}
		return adapter.NamespaceRef{}, createErr
	}
	return ref, nil
}

// CreateRepoFromTemplate creates a repository from a template using Forgejo's
// "generate" endpoint. The call is idempotent: if the repo already exists it is
// returned as-is.
//
// Limitation: Gitea's generate API copies only the default branch (git_content:
// true). opts.IncludeAllBranches is accepted but cannot be honoured. The template
// repository must be marked as a template on the Forgejo/Gitea instance.
func (a *Adapter) CreateRepoFromTemplate(ctx context.Context, tmpl adapter.TemplateRef, ns adapter.NamespaceRef, name string, opts adapter.CreateRepoOptions) (adapter.RepoRef, error) {
	ref := adapter.RepoRef{Host: a.host, Namespace: ns.Slug, Name: name}
	// Idempotent: return the ref if the repo already exists.
	exists, err := a.RepoExists(ctx, ref)
	if err != nil {
		return adapter.RepoRef{}, err
	}
	if exists {
		return ref, nil
	}
	in := map[string]any{
		"owner":       ns.Slug,
		"name":        name,
		"private":     opts.Private,
		"description": opts.Description,
		"git_content": true, // copy repository content (default branch only)
	}
	path := "/repos/" + tmpl.Namespace + "/" + tmpl.Name + "/generate"
	if createErr := a.do(ctx, http.MethodPost, path, in, nil, http.StatusCreated); createErr != nil {
		// Gitea uses 409 for an already-existing repo and 422 for validation
		// errors — but 422 also fires for genuine failures (template not marked
		// as a template, invalid name, etc.). Don't infer "already exists" from
		// the status alone; confirm with a RepoExists check.
		s := statusOf(createErr)
		if s == http.StatusConflict || s == http.StatusUnprocessableEntity {
			if ok, _ := a.RepoExists(ctx, ref); ok {
				return ref, nil // repo exists (created concurrently or pre-existing)
			}
		}
		return adapter.RepoRef{}, createErr
	}
	return ref, nil
}

// RepoExists reports whether the repository exists.
func (a *Adapter) RepoExists(ctx context.Context, repo adapter.RepoRef) (bool, error) {
	err := a.do(ctx, http.MethodGet, "/repos/"+repo.Namespace+"/"+repo.Name, nil, nil, http.StatusOK)
	if err == nil {
		return true, nil
	}
	if statusOf(err) == http.StatusNotFound {
		return false, nil
	}
	return false, err
}

// SetCollaborator grants the given role to username on repo.
func (a *Adapter) SetCollaborator(ctx context.Context, repo adapter.RepoRef, username string, role adapter.Role) error {
	perms := map[adapter.Role]string{
		adapter.RoleRead:  "read",
		adapter.RoleWrite: "write",
		adapter.RoleAdmin: "admin",
	}
	perm := perms[role]
	if perm == "" {
		perm = "write"
	}
	path := "/repos/" + repo.Namespace + "/" + repo.Name + "/collaborators/" + username
	return a.do(ctx, http.MethodPut, path, map[string]string{"permission": perm}, nil, http.StatusNoContent)
}

// RemoveCollaborator revokes username's access to repo.
func (a *Adapter) RemoveCollaborator(ctx context.Context, repo adapter.RepoRef, username string) error {
	path := "/repos/" + repo.Namespace + "/" + repo.Name + "/collaborators/" + username
	return a.do(ctx, http.MethodDelete, path, nil, nil, http.StatusNoContent)
}

// LatestCommit returns the most recent commit on the given branch (empty = default
// branch). An empty repository (Forgejo returns 404 or 409) yields a zero Commit
// and a nil error.
func (a *Adapter) LatestCommit(ctx context.Context, repo adapter.RepoRef, branch string) (adapter.Commit, error) {
	path := "/repos/" + repo.Namespace + "/" + repo.Name + "/commits?limit=1"
	if branch != "" {
		path += "&sha=" + branch
	}
	var commits []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message   string `json:"message"`
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
		Author *struct {
			Login string `json:"login"`
		} `json:"author"`
	}
	if err := a.do(ctx, http.MethodGet, path, nil, &commits, http.StatusOK); err != nil {
		// 404 or 409: repository is empty (no commits yet).
		s := statusOf(err)
		if s == http.StatusNotFound || s == http.StatusConflict {
			return adapter.Commit{}, nil
		}
		return adapter.Commit{}, err
	}
	if len(commits) == 0 {
		return adapter.Commit{}, nil
	}
	c := commits[0]
	out := adapter.Commit{
		SHA:       c.SHA,
		Message:   c.Commit.Message,
		Timestamp: c.Commit.Committer.Date,
	}
	if c.Author != nil {
		out.AuthorUsername = c.Author.Login
	}
	return out, nil
}

// LockRepo archives the repository, preventing new pushes. The token must have
// admin access to the repo.
func (a *Adapter) LockRepo(ctx context.Context, repo adapter.RepoRef) error {
	return a.do(ctx, http.MethodPatch, "/repos/"+repo.Namespace+"/"+repo.Name,
		map[string]bool{"archived": true}, nil, http.StatusOK)
}

// UnlockRepo un-archives the repository, restoring push access.
func (a *Adapter) UnlockRepo(ctx context.Context, repo adapter.RepoRef) error {
	return a.do(ctx, http.MethodPatch, "/repos/"+repo.Namespace+"/"+repo.Name,
		map[string]bool{"archived": false}, nil, http.StatusOK)
}

// EnsureWebhook makes sure a webhook matching spec.URL exists on repo. If the
// webhook is already registered the call is a no-op.
func (a *Adapter) EnsureWebhook(ctx context.Context, repo adapter.RepoRef, spec adapter.WebhookSpec) error {
	base := "/repos/" + repo.Namespace + "/" + repo.Name + "/hooks"
	var hooks []struct {
		ID     int64 `json:"id"`
		Config struct {
			URL string `json:"url"`
		} `json:"config"`
	}
	if err := a.do(ctx, http.MethodGet, base, nil, &hooks, http.StatusOK); err != nil {
		return err
	}
	for _, h := range hooks {
		if h.Config.URL == spec.URL {
			return nil // already present; idempotent
		}
	}
	events := spec.Events
	if len(events) == 0 {
		events = []string{"push"}
	}
	in := map[string]any{
		"type":   "gitea",
		"active": true,
		"events": events,
		"config": map[string]string{
			"url":          spec.URL,
			"content_type": "json",
			"secret":       spec.Secret,
		},
	}
	return a.do(ctx, http.MethodPost, base, in, nil, http.StatusCreated)
}

// DispatchGrading returns ErrNotImplemented. Forgejo Actions could dispatch
// grading natively in a future phase; for now Cairn's own sandboxed runners
// handle all grading.
func (a *Adapter) DispatchGrading(_ context.Context, _ adapter.GradingDispatch) error {
	return adapter.ErrNotImplemented
}

// GradingResult reads the combined commit status for sha and maps it to a
// CheckResult. Numeric scores are not populated here; they come from Cairn's
// runner path.
func (a *Adapter) GradingResult(ctx context.Context, repo adapter.RepoRef, sha string) (adapter.CheckResult, error) {
	path := "/repos/" + repo.Namespace + "/" + repo.Name + "/commits/" + sha + "/status"
	var res struct {
		State      string `json:"state"`
		TotalCount int    `json:"total_count"`
		Statuses   []struct {
			TargetURL string `json:"target_url"`
		} `json:"statuses"`
	}
	if err := a.do(ctx, http.MethodGet, path, nil, &res, http.StatusOK); err != nil {
		return adapter.CheckResult{}, err
	}
	out := adapter.CheckResult{}
	if len(res.Statuses) > 0 {
		out.DetailURL = res.Statuses[0].TargetURL
	}
	if res.TotalCount == 0 {
		out.Status = adapter.CheckPending
		return out, nil
	}
	switch res.State {
	case "success":
		out.Status = adapter.CheckPassed
	case "failure":
		out.Status = adapter.CheckFailed
	case "error":
		out.Status = adapter.CheckError
	default:
		out.Status = adapter.CheckPending
	}
	return out, nil
}
