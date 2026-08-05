// SPDX-License-Identifier: Apache-2.0

// Package github implements adapter.Adapter for GitHub.
//
// It authenticates as a GitHub App, minting and caching short-lived installation
// access tokens on demand — no long-lived tokens are stored. REST calls use the
// standard library only, keeping this package dependency-free in line with the
// project's goal of a lean, auditable adapter layer. A typed client such as
// go-github could be swapped in behind the same adapter.Adapter interface without
// affecting the rest of the codebase.
package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

const defaultAPIBase = "https://api.github.com"

// tokenClient is used exclusively for minting GitHub App installation tokens.
// A dedicated client with a hard timeout prevents a hung GitHub token endpoint
// from deadlocking the installTransport mutex and stalling all API workers.
var tokenClient = &http.Client{Timeout: 30 * time.Second}

// Config holds GitHub App credentials for a single installation.
type Config struct {
	AppID          int64
	InstallationID int64
	PrivateKeyPEM  []byte
	BaseURL        string // optional; defaults to https://api.github.com (set for GHES)
}

// Adapter implements adapter.Adapter against GitHub.
type Adapter struct {
	httpc   *http.Client
	baseURL string
}

// Compile-time guarantee that *Adapter stays in sync with the interface.
var _ adapter.Adapter = (*Adapter)(nil)

// New constructs a GitHub adapter authenticated as a GitHub App installation.
func New(cfg Config) (*Adapter, error) {
	key, err := parsePrivateKey(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("github: parse private key: %w", err)
	}
	base := cfg.BaseURL
	if base == "" {
		base = defaultAPIBase
	}
	tr := &installTransport{
		base:    http.DefaultTransport,
		apiBase: base,
		appID:   cfg.AppID,
		instID:  cfg.InstallationID,
		key:     key,
	}
	return &Adapter{
		httpc:   &http.Client{Transport: tr, Timeout: 30 * time.Second},
		baseURL: base,
	}, nil
}

// Host returns the host this adapter targets.
func (a *Adapter) Host() adapter.Host { return adapter.HostGitHub }

// RepoWebURL returns the browser URL for a repo. For github.com the web host is
// github.com (distinct from the api.github.com API host); for GHES the API base
// is https://host/api/v3, so the web host is that with the /api/v3 suffix removed.
func (a *Adapter) RepoWebURL(repo adapter.RepoRef) string {
	web := "https://github.com"
	if a.baseURL != defaultAPIBase {
		web = strings.TrimSuffix(strings.TrimRight(a.baseURL, "/"), "/api/v3")
	}
	return web + "/" + repo.Namespace + "/" + repo.Name
}

// --- authentication -------------------------------------------------------

// installTransport injects a GitHub App installation token into every request,
// refreshing it shortly before expiry. It is safe for concurrent use.
type installTransport struct {
	base    http.RoundTripper
	apiBase string
	appID   int64
	instID  int64
	key     *rsa.PrivateKey

	mu    sync.Mutex
	token string
	exp   time.Time
}

func (t *installTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := t.installationToken(req.Context())
	if err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "token "+tok)
	if r.Header.Get("Accept") == "" {
		r.Header.Set("Accept", "application/vnd.github+json")
	}
	r.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	return t.base.RoundTrip(r)
}

func (t *installTransport) installationToken(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Until(t.exp) > time.Minute {
		return t.token, nil
	}
	jwt, err := appJWT(t.appID, t.key)
	if err != nil {
		return "", err
	}
	url := t.apiBase + "/app/installations/" + strconv.FormatInt(t.instID, 10) + "/access_tokens"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := tokenClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", &apiError{Status: resp.StatusCode, Body: string(body)}
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	t.token, t.exp = out.Token, out.ExpiresAt
	return t.token, nil
}

// appJWT mints a short-lived RS256 JWT asserting the App's identity.
func appJWT(appID int64, key *rsa.PrivateKey) (string, error) {
	now := time.Now()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iat": now.Add(-30 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": strconv.FormatInt(appID, 10),
	})
	signingInput := b64(header) + "." + b64(claims)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + b64(sig), nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func parsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	k, ok := keyAny.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an RSA private key")
	}
	return k, nil
}

// --- HTTP plumbing --------------------------------------------------------

type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("github api: status %d: %s", e.Status, e.Body)
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

func (a *Adapter) EnsureNamespace(ctx context.Context, slug string) (adapter.NamespaceRef, error) {
	// GitHub orgs cannot be created through the REST API, so we verify the org
	// exists rather than create it.
	err := a.do(ctx, http.MethodGet, "/orgs/"+slug, nil, nil, http.StatusOK)
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return adapter.NamespaceRef{}, fmt.Errorf("github: organization %q not found — create it on GitHub and install the App on it", slug)
		}
		return adapter.NamespaceRef{}, err
	}
	return adapter.NamespaceRef{Host: adapter.HostGitHub, Slug: slug}, nil
}

func (a *Adapter) CreateRepoFromTemplate(ctx context.Context, tmpl adapter.TemplateRef, ns adapter.NamespaceRef, name string, opts adapter.CreateRepoOptions) (adapter.RepoRef, error) {
	ref := adapter.RepoRef{Host: adapter.HostGitHub, Namespace: ns.Slug, Name: name}
	// Idempotent: a repo that already exists is returned as-is.
	exists, err := a.RepoExists(ctx, ref)
	if err != nil {
		return adapter.RepoRef{}, err
	}
	if exists {
		return ref, nil
	}
	in := map[string]any{
		"owner":                ns.Slug,
		"name":                 name,
		"private":              opts.Private,
		"include_all_branches": opts.IncludeAllBranches,
		"description":          opts.Description,
	}
	path := "/repos/" + tmpl.Namespace + "/" + tmpl.Name + "/generate"
	if err := a.do(ctx, http.MethodPost, path, in, nil, http.StatusCreated); err != nil {
		// 422 fires when the repo name already exists. Confirm with a RepoExists
		// check rather than trusting the status code alone.
		if statusOf(err) == http.StatusUnprocessableEntity {
			if ok, _ := a.RepoExists(ctx, ref); ok {
				return ref, nil
			}
		}
		return adapter.RepoRef{}, err
	}
	return ref, nil
}

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

func (a *Adapter) SetCollaborator(ctx context.Context, repo adapter.RepoRef, username string, role adapter.Role) error {
	perms := map[adapter.Role]string{
		adapter.RoleRead:  "pull",
		adapter.RoleWrite: "push",
		adapter.RoleAdmin: "admin",
	}
	perm := perms[role]
	if perm == "" {
		perm = "push"
	}
	path := "/repos/" + repo.Namespace + "/" + repo.Name + "/collaborators/" + username
	// 201 = invitation created; 204 = already a collaborator.
	return a.do(ctx, http.MethodPut, path, map[string]string{"permission": perm}, nil, http.StatusCreated, http.StatusNoContent)
}

func (a *Adapter) RemoveCollaborator(ctx context.Context, repo adapter.RepoRef, username string) error {
	path := "/repos/" + repo.Namespace + "/" + repo.Name + "/collaborators/" + username
	return a.do(ctx, http.MethodDelete, path, nil, nil, http.StatusNoContent)
}

func (a *Adapter) LatestCommit(ctx context.Context, repo adapter.RepoRef, branch string) (adapter.Commit, error) {
	path := "/repos/" + repo.Namespace + "/" + repo.Name + "/commits?per_page=1"
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
		// 409 Conflict: the repository is empty (no commits yet).
		if statusOf(err) == http.StatusConflict {
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

func (a *Adapter) LockRepo(ctx context.Context, repo adapter.RepoRef) error {
	// MVP deadline lock: archive the repo. This is read-only, reversible, and
	// works on every plan. A branch-protection / ruleset lock is a lighter-touch
	// alternative for orgs whose plan supports it.
	return a.do(ctx, http.MethodPatch, "/repos/"+repo.Namespace+"/"+repo.Name, map[string]bool{"archived": true}, nil, http.StatusOK)
}

func (a *Adapter) UnlockRepo(ctx context.Context, repo adapter.RepoRef) error {
	return a.do(ctx, http.MethodPatch, "/repos/"+repo.Namespace+"/"+repo.Name, map[string]bool{"archived": false}, nil, http.StatusOK)
}

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
		"name":   "web",
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

func (a *Adapter) DispatchGrading(_ context.Context, _ adapter.GradingDispatch) error {
	// GitHub repos that contain an Actions workflow grade automatically on push.
	// For the portable-runner path, returning ErrNotImplemented signals the
	// orchestrator to grade on Cairn's own sandboxed runners.
	return adapter.ErrNotImplemented
}

func (a *Adapter) GradingResult(ctx context.Context, repo adapter.RepoRef, sha string) (adapter.CheckResult, error) {
	path := "/repos/" + repo.Namespace + "/" + repo.Name + "/commits/" + sha + "/check-runs"
	var res struct {
		CheckRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
		} `json:"check_runs"`
	}
	if err := a.do(ctx, http.MethodGet, path, nil, &res, http.StatusOK); err != nil {
		return adapter.CheckResult{}, err
	}
	if len(res.CheckRuns) == 0 {
		return adapter.CheckResult{Status: adapter.CheckPending}, nil
	}
	allDone, anyFail, detail := true, false, ""
	for _, r := range res.CheckRuns {
		if detail == "" {
			detail = r.HTMLURL
		}
		if r.Status != "completed" {
			allDone = false
		}
		switch r.Conclusion {
		case "", "success", "neutral", "skipped":
			// not a failure
		default:
			anyFail = true
		}
	}
	out := adapter.CheckResult{DetailURL: detail}
	switch {
	case !allDone:
		out.Status = adapter.CheckRunning
	case anyFail:
		out.Status = adapter.CheckFailed
	default:
		out.Status = adapter.CheckPassed
	}
	// Numeric Score/MaxScore are left nil: they come from Cairn's runner path or a
	// later Actions-output convention, not from the bare check-run status.
	return out, nil
}
