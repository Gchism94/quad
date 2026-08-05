// SPDX-License-Identifier: Apache-2.0

package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

// assertAuth checks that the request carries the expected bearer token.
func assertAuth(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "token t" {
		t.Errorf("Authorization = %q, want %q", got, "token t")
	}
}

// newTestAdapter creates an Adapter wired to srv.URL with token "t".
func newTestAdapter(t *testing.T, srv *httptest.Server) *Adapter {
	t.Helper()
	a, err := New(Config{BaseURL: srv.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// bg is a package-level background context used in all tests.
var bg = context.Background()

// --- New / Host -----------------------------------------------------------

func TestNewRejectsEmptyBaseURL(t *testing.T) {
	if _, err := New(Config{Token: "x"}); err == nil {
		t.Fatal("want error for empty BaseURL")
	}
}

func TestNewRejectsEmptyToken(t *testing.T) {
	if _, err := New(Config{BaseURL: "http://localhost"}); err == nil {
		t.Fatal("want error for empty Token")
	}
}

func TestHostIsForgejo(t *testing.T) {
	a, err := New(Config{BaseURL: "http://localhost", Token: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Host() != adapter.HostForgejo {
		t.Fatalf("Host() = %q, want %q", a.Host(), adapter.HostForgejo)
	}
}

// --- EnsureNamespace ------------------------------------------------------

func TestEnsureNamespaceExisting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAuth(t, r)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/orgs/cs101" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"name":"cs101"}`))
	}))
	defer srv.Close()
	a := newTestAdapter(t, srv)

	ref, err := a.EnsureNamespace(bg, "cs101")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Host != adapter.HostForgejo || ref.Slug != "cs101" {
		t.Fatalf("ref = %+v", ref)
	}
}

func TestEnsureNamespaceCreate(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAuth(t, r)
		calls++
		switch calls {
		case 1: // GET → 404
			if r.Method != http.MethodGet || r.URL.Path != "/api/v1/orgs/neworg" {
				t.Errorf("call 1: unexpected %s %s", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNotFound)
		case 2: // POST /orgs → 201, assert body
			if r.Method != http.MethodPost || r.URL.Path != "/api/v1/orgs" {
				t.Errorf("call 2: unexpected %s %s", r.Method, r.URL.Path)
			}
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["username"] != "neworg" {
				t.Errorf("body.username = %q, want neworg", body["username"])
			}
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"name":"neworg"}`))
		default:
			t.Errorf("unexpected call %d: %s %s", calls, r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	a := newTestAdapter(t, srv)

	ref, err := a.EnsureNamespace(bg, "neworg")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Slug != "neworg" {
		t.Fatalf("ref.Slug = %q, want neworg", ref.Slug)
	}
	if calls != 2 {
		t.Fatalf("want 2 calls (GET + POST), got %d", calls)
	}
}

// TestEnsureNamespaceConflict exercises the confirm-on-error paths for
// EnsureNamespace: a 409 or 422 from POST /orgs triggers a re-GET to
// distinguish a concurrent creation (org now exists → success) from a genuine
// validation error (org still missing → propagate the original error).
func TestEnsureNamespaceConflict(t *testing.T) {
	cases := []struct {
		name        string
		postStatus  int  // what POST /orgs returns
		getConfirms bool // whether the re-GET finds the org
		wantErr     bool
	}{
		{"409 org exists (concurrent)", http.StatusConflict, true, false},
		{"422 org exists (concurrent)", http.StatusUnprocessableEntity, true, false},
		{"422 genuine validation error", http.StatusUnprocessableEntity, false, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch {
				case calls == 1 && r.Method == http.MethodGet: // initial GET → 404
					w.WriteHeader(http.StatusNotFound)
				case calls == 2 && r.Method == http.MethodPost: // POST → conflict status
					w.WriteHeader(tc.postStatus)
					w.Write([]byte(`{"message":"conflict"}`))
				case calls == 3 && r.Method == http.MethodGet: // confirmation re-GET
					if tc.getConfirms {
						w.WriteHeader(http.StatusOK)
						w.Write([]byte(`{"name":"org"}`))
					} else {
						w.WriteHeader(http.StatusNotFound)
					}
				default:
					t.Errorf("unexpected call %d: %s %s", calls, r.Method, r.URL.Path)
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			defer srv.Close()
			a := newTestAdapter(t, srv)
			_, err := a.EnsureNamespace(bg, "org")
			if tc.wantErr && err == nil {
				t.Error("want error for genuine validation failure, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("want nil error for idempotent case, got %v", err)
			}
		})
	}
}

// --- CreateRepoFromTemplate -----------------------------------------------

func TestCreateRepoFromTemplateFresh(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAuth(t, r)
		calls++
		switch calls {
		case 1: // RepoExists GET → 404
			w.WriteHeader(http.StatusNotFound)
		case 2: // generate POST → 201
			if r.Method != http.MethodPost {
				t.Errorf("call 2: want POST, got %s", r.Method)
			}
			if !strings.HasSuffix(r.URL.Path, "/generate") {
				t.Errorf("call 2: path %q should end with /generate", r.URL.Path)
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["owner"] != "cs101-org" {
				t.Errorf("owner = %v, want cs101-org", body["owner"])
			}
			if body["name"] != "hw1-alice" {
				t.Errorf("name = %v, want hw1-alice", body["name"])
			}
			if body["git_content"] != true {
				t.Errorf("git_content = %v, want true", body["git_content"])
			}
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected call %d: %s %s", calls, r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	a := newTestAdapter(t, srv)

	ref, err := a.CreateRepoFromTemplate(bg,
		adapter.TemplateRef{Host: adapter.HostForgejo, Namespace: "cs101-org", Name: "hw1-template"},
		adapter.NamespaceRef{Host: adapter.HostForgejo, Slug: "cs101-org"},
		"hw1-alice",
		adapter.CreateRepoOptions{Private: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Host != adapter.HostForgejo || ref.Namespace != "cs101-org" || ref.Name != "hw1-alice" {
		t.Fatalf("ref = %+v", ref)
	}
	if calls != 2 {
		t.Fatalf("want 2 calls, got %d", calls)
	}
}

func TestCreateRepoFromTemplateIdempotent(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAuth(t, r)
		calls++
		if r.Method != http.MethodGet {
			t.Errorf("idempotent case: unexpected %s (want only GET)", r.Method)
		}
		w.WriteHeader(http.StatusOK) // repo exists
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	a := newTestAdapter(t, srv)

	ref, err := a.CreateRepoFromTemplate(bg,
		adapter.TemplateRef{Host: adapter.HostForgejo, Namespace: "org", Name: "tmpl"},
		adapter.NamespaceRef{Host: adapter.HostForgejo, Slug: "org"},
		"existing-repo",
		adapter.CreateRepoOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Name != "existing-repo" {
		t.Fatalf("ref.Name = %q", ref.Name)
	}
	if calls != 1 {
		t.Fatalf("idempotent: want 1 GET, got %d calls", calls)
	}
}

// --- Gitea host label -----------------------------------------------------

// TestGiteaHostReported confirms an adapter constructed for HostGitea reports it.
func TestGiteaHostReported(t *testing.T) {
	a, err := NewWithHost(Config{BaseURL: "http://localhost", Token: "t"}, adapter.HostGitea)
	if err != nil {
		t.Fatal(err)
	}
	if a.Host() != adapter.HostGitea {
		t.Fatalf("Host() = %q, want %q", a.Host(), adapter.HostGitea)
	}
	// The default constructor still reports Forgejo (backward compatible).
	def, _ := New(Config{BaseURL: "http://localhost", Token: "t"})
	if def.Host() != adapter.HostForgejo {
		t.Fatalf("New().Host() = %q, want %q", def.Host(), adapter.HostForgejo)
	}
}

// TestGiteaHostStampedEndToEnd asserts that a Gitea-constructed adapter (i) hits
// the identical /api/v1 paths as the Forgejo path and (ii) stamps HostGitea onto
// the namespace and repo refs it returns. This is the regression guard that the
// "gitea" label is honored end to end through the shared implementation.
func TestGiteaHostStampedEndToEnd(t *testing.T) {
	var paths []string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAuth(t, r)
		paths = append(paths, r.URL.Path)
		calls++
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/orgs/cs101-org"):
			w.WriteHeader(http.StatusOK) // org exists
			w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/repos/cs101-org/hw1-gitea"):
			w.WriteHeader(http.StatusNotFound) // RepoExists → 404
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/generate"):
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected call %d: %s %s", calls, r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	a, err := NewWithHost(Config{BaseURL: srv.URL, Token: "t"}, adapter.HostGitea)
	if err != nil {
		t.Fatal(err)
	}

	ns, err := a.EnsureNamespace(bg, "cs101-org")
	if err != nil {
		t.Fatal(err)
	}
	if ns.Host != adapter.HostGitea {
		t.Errorf("NamespaceRef.Host = %q, want %q", ns.Host, adapter.HostGitea)
	}

	ref, err := a.CreateRepoFromTemplate(bg,
		adapter.TemplateRef{Host: adapter.HostGitea, Namespace: "cs101-org", Name: "hw1-template"},
		ns,
		"hw1-gitea",
		adapter.CreateRepoOptions{Private: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Host != adapter.HostGitea {
		t.Errorf("RepoRef.Host = %q, want %q", ref.Host, adapter.HostGitea)
	}

	// Every request must target the shared /api/v1 surface — identical to Forgejo.
	for _, p := range paths {
		if !strings.HasPrefix(p, "/api/v1/") {
			t.Errorf("request path %q does not use the /api/v1 surface", p)
		}
	}
}

// TestCreateRepoFromTemplateConflict exercises the confirm-on-error paths for
// CreateRepoFromTemplate: a 409 or 422 from POST .../generate triggers a
// RepoExists check to distinguish a concurrent creation from a genuine error.
func TestCreateRepoFromTemplateConflict(t *testing.T) {
	cases := []struct {
		name       string
		postStatus int
		repoExists bool
		wantErr    bool
	}{
		{"409 repo exists (concurrent)", http.StatusConflict, true, false},
		{"422 repo exists (concurrent)", http.StatusUnprocessableEntity, true, false},
		{"422 template not marked as template", http.StatusUnprocessableEntity, false, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch {
				case calls == 1 && r.Method == http.MethodGet: // initial RepoExists → 404
					w.WriteHeader(http.StatusNotFound)
				case calls == 2 && r.Method == http.MethodPost: // generate → conflict status
					w.WriteHeader(tc.postStatus)
					w.Write([]byte(`{"message":"conflict"}`))
				case calls == 3 && r.Method == http.MethodGet: // confirmation RepoExists
					if tc.repoExists {
						w.WriteHeader(http.StatusOK)
						w.Write([]byte(`{}`))
					} else {
						w.WriteHeader(http.StatusNotFound)
					}
				default:
					t.Errorf("unexpected call %d: %s %s", calls, r.Method, r.URL.Path)
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			defer srv.Close()
			a := newTestAdapter(t, srv)
			_, err := a.CreateRepoFromTemplate(bg,
				adapter.TemplateRef{Host: adapter.HostForgejo, Namespace: "org", Name: "tmpl"},
				adapter.NamespaceRef{Host: adapter.HostForgejo, Slug: "org"},
				"repo",
				adapter.CreateRepoOptions{},
			)
			if tc.wantErr && err == nil {
				t.Error("want error for genuine failure, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("want nil error for idempotent case, got %v", err)
			}
		})
	}
}

// --- RepoExists -----------------------------------------------------------

func TestRepoExistsTrue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAuth(t, r)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	a := newTestAdapter(t, srv)
	ok, err := a.RepoExists(bg, adapter.RepoRef{Host: adapter.HostForgejo, Namespace: "org", Name: "repo"})
	if err != nil || !ok {
		t.Fatalf("RepoExists = %v, %v; want true, nil", ok, err)
	}
}

func TestRepoExistsFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	a := newTestAdapter(t, srv)
	ok, err := a.RepoExists(bg, adapter.RepoRef{Host: adapter.HostForgejo, Namespace: "org", Name: "missing"})
	if err != nil || ok {
		t.Fatalf("RepoExists = %v, %v; want false, nil", ok, err)
	}
}

// --- SetCollaborator ------------------------------------------------------

func TestSetCollaborator(t *testing.T) {
	cases := []struct {
		role adapter.Role
		want string
	}{
		{adapter.RoleRead, "read"},
		{adapter.RoleWrite, "write"},
		{adapter.RoleAdmin, "admin"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.role), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertAuth(t, r)
				if r.Method != http.MethodPut {
					t.Errorf("want PUT, got %s", r.Method)
				}
				if !strings.HasSuffix(r.URL.Path, "/collaborators/alice") {
					t.Errorf("path %q should end with /collaborators/alice", r.URL.Path)
				}
				var body map[string]string
				json.NewDecoder(r.Body).Decode(&body)
				if body["permission"] != tc.want {
					t.Errorf("permission = %q, want %q", body["permission"], tc.want)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()
			a := newTestAdapter(t, srv)
			if err := a.SetCollaborator(bg, adapter.RepoRef{Host: adapter.HostForgejo, Namespace: "org", Name: "repo"}, "alice", tc.role); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// --- RemoveCollaborator ---------------------------------------------------

func TestRemoveCollaborator(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAuth(t, r)
		if r.Method != http.MethodDelete {
			t.Errorf("want DELETE, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/collaborators/bob") {
			t.Errorf("path %q should end with /collaborators/bob", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	a := newTestAdapter(t, srv)
	if err := a.RemoveCollaborator(bg, adapter.RepoRef{Host: adapter.HostForgejo, Namespace: "org", Name: "repo"}, "bob"); err != nil {
		t.Fatal(err)
	}
}

// --- LatestCommit ---------------------------------------------------------

func TestLatestCommitOneCommit(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	payload := []map[string]any{
		{
			"sha": "abc123",
			"commit": map[string]any{
				"message": "feat: hello world",
				"committer": map[string]any{
					"date": ts.Format(time.RFC3339),
				},
			},
			"author": map[string]any{"login": "alice"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAuth(t, r)
		if !strings.Contains(r.URL.RawQuery, "limit=1") {
			t.Errorf("want limit=1 in query, got %q", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()
	a := newTestAdapter(t, srv)
	c, err := a.LatestCommit(bg, adapter.RepoRef{Host: adapter.HostForgejo, Namespace: "org", Name: "repo"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.SHA != "abc123" || c.Message != "feat: hello world" || c.AuthorUsername != "alice" {
		t.Fatalf("commit = %+v", c)
	}
	if !c.Timestamp.Equal(ts) {
		t.Fatalf("timestamp = %v, want %v", c.Timestamp, ts)
	}
}

func TestLatestCommitEmptyRepo404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	a := newTestAdapter(t, srv)
	c, err := a.LatestCommit(bg, adapter.RepoRef{Host: adapter.HostForgejo, Namespace: "org", Name: "empty"}, "")
	if err != nil {
		t.Fatalf("empty-repo 404: want nil err, got %v", err)
	}
	if c.SHA != "" {
		t.Fatalf("empty-repo 404: want zero Commit, got %+v", c)
	}
}

func TestLatestCommitEmptyRepo409(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"message":"repository is empty"}`))
	}))
	defer srv.Close()
	a := newTestAdapter(t, srv)
	c, err := a.LatestCommit(bg, adapter.RepoRef{Host: adapter.HostForgejo, Namespace: "org", Name: "empty"}, "")
	if err != nil {
		t.Fatalf("empty-repo 409: want nil err, got %v", err)
	}
	if c.SHA != "" {
		t.Fatalf("empty-repo 409: want zero Commit, got %+v", c)
	}
}

// --- LockRepo / UnlockRepo ------------------------------------------------

func TestLockRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAuth(t, r)
		if r.Method != http.MethodPatch {
			t.Errorf("want PATCH, got %s", r.Method)
		}
		var body map[string]bool
		json.NewDecoder(r.Body).Decode(&body)
		if !body["archived"] {
			t.Error("want archived=true for LockRepo")
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	a := newTestAdapter(t, srv)
	if err := a.LockRepo(bg, adapter.RepoRef{Host: adapter.HostForgejo, Namespace: "org", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
}

func TestUnlockRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAuth(t, r)
		var body map[string]bool
		json.NewDecoder(r.Body).Decode(&body)
		if body["archived"] {
			t.Error("want archived=false for UnlockRepo")
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	a := newTestAdapter(t, srv)
	if err := a.UnlockRepo(bg, adapter.RepoRef{Host: adapter.HostForgejo, Namespace: "org", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
}

// --- EnsureWebhook --------------------------------------------------------

func TestEnsureWebhookCreate(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAuth(t, r)
		calls++
		switch calls {
		case 1: // GET list → empty
			if r.Method != http.MethodGet {
				t.Errorf("call 1: want GET, got %s", r.Method)
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		case 2: // POST → assert config
			if r.Method != http.MethodPost {
				t.Errorf("call 2: want POST, got %s", r.Method)
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["type"] != "gitea" {
				t.Errorf("type = %v, want gitea", body["type"])
			}
			cfg, _ := body["config"].(map[string]any)
			if cfg["url"] != "https://cairn.example/hook" {
				t.Errorf("config.url = %v", cfg["url"])
			}
			if cfg["content_type"] != "json" {
				t.Errorf("config.content_type = %v", cfg["content_type"])
			}
			if cfg["secret"] != "mysecret" {
				t.Errorf("config.secret = %v", cfg["secret"])
			}
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":1}`))
		default:
			t.Errorf("unexpected call %d", calls)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	a := newTestAdapter(t, srv)
	if err := a.EnsureWebhook(bg,
		adapter.RepoRef{Host: adapter.HostForgejo, Namespace: "org", Name: "repo"},
		adapter.WebhookSpec{URL: "https://cairn.example/hook", Secret: "mysecret"},
	); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("want 2 calls (GET + POST), got %d", calls)
	}
}

func TestEnsureWebhookIdempotent(t *testing.T) {
	calls := 0
	existing := []map[string]any{
		{"id": 1, "config": map[string]any{"url": "https://cairn.example/hook"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet {
			t.Errorf("idempotent webhook: unexpected %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(existing)
	}))
	defer srv.Close()
	a := newTestAdapter(t, srv)
	if err := a.EnsureWebhook(bg,
		adapter.RepoRef{Host: adapter.HostForgejo, Namespace: "org", Name: "repo"},
		adapter.WebhookSpec{URL: "https://cairn.example/hook"},
	); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("idempotent: want 1 GET, got %d", calls)
	}
}

// --- DispatchGrading ------------------------------------------------------

func TestDispatchGradingNotImplemented(t *testing.T) {
	a, err := New(Config{BaseURL: "http://localhost", Token: "x"})
	if err != nil {
		t.Fatal(err)
	}
	err = a.DispatchGrading(bg, adapter.GradingDispatch{})
	if !errors.Is(err, adapter.ErrNotImplemented) {
		t.Fatalf("DispatchGrading = %v, want ErrNotImplemented", err)
	}
}

// --- GradingResult --------------------------------------------------------

func TestGradingResult(t *testing.T) {
	cases := []struct {
		state      string
		totalCount int
		wantStatus adapter.CheckStatus
	}{
		{"", 0, adapter.CheckPending},
		{"success", 1, adapter.CheckPassed},
		{"failure", 1, adapter.CheckFailed},
		{"error", 1, adapter.CheckError},
		{"pending", 1, adapter.CheckPending},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.state+"/total="+func() string {
			if tc.totalCount == 0 {
				return "0"
			}
			return "1"
		}(), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertAuth(t, r)
				if !strings.Contains(r.URL.Path, "/status") {
					t.Errorf("path %q should contain /status", r.URL.Path)
				}
				payload := map[string]any{
					"state":       tc.state,
					"total_count": tc.totalCount,
					"statuses":    []map[string]any{{"target_url": "https://ci.example/run/1"}},
				}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(payload)
			}))
			defer srv.Close()
			a := newTestAdapter(t, srv)
			res, err := a.GradingResult(bg, adapter.RepoRef{Host: adapter.HostForgejo, Namespace: "org", Name: "repo"}, "deadbeef")
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", res.Status, tc.wantStatus)
			}
			// DetailURL populated from statuses[0] when total_count > 0
			if tc.totalCount > 0 && res.DetailURL != "https://ci.example/run/1" {
				t.Errorf("DetailURL = %q, want https://ci.example/run/1", res.DetailURL)
			}
		})
	}
}

// TestEnsureNamespace422BodySurfaced verifies that when a 422 from POST /orgs
// is followed by a re-GET that does NOT confirm org existence, the error
// returned to the caller includes the original 422 response body.
func TestEnsureNamespace422BodySurfaced(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case calls == 1 && r.Method == http.MethodGet: // initial GET → 404
			w.WriteHeader(http.StatusNotFound)
		case calls == 2 && r.Method == http.MethodPost: // POST → 422 validation error
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"invalid organization name"}`))
		case calls == 3 && r.Method == http.MethodGet: // re-GET → still 404
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected call %d: %s %s", calls, r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	a := newTestAdapter(t, srv)

	_, err := a.EnsureNamespace(bg, "bad-org")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid organization name") {
		t.Errorf("error %q does not contain original 422 body", err.Error())
	}
}

// TestCreateRepoFromTemplate422BodySurfaced verifies that when a 422 from the
// generate endpoint is NOT followed by a confirming RepoExists, the error
// includes the original 422 response body (e.g. "not a template").
func TestCreateRepoFromTemplate422BodySurfaced(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case calls == 1 && r.Method == http.MethodGet: // RepoExists → 404
			w.WriteHeader(http.StatusNotFound)
		case calls == 2 && r.Method == http.MethodPost: // generate → 422
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"template repository is not marked as a template"}`))
		case calls == 3 && r.Method == http.MethodGet: // confirmation RepoExists → 404
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected call %d: %s %s", calls, r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	a := newTestAdapter(t, srv)

	_, err := a.CreateRepoFromTemplate(bg,
		adapter.TemplateRef{Host: adapter.HostForgejo, Namespace: "org", Name: "not-a-template"},
		adapter.NamespaceRef{Host: adapter.HostForgejo, Slug: "org"},
		"new-repo",
		adapter.CreateRepoOptions{},
	)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "not marked as a template") {
		t.Errorf("error %q does not contain original 422 body", err.Error())
	}
}

func TestRepoWebURL(t *testing.T) {
	a, err := New(Config{BaseURL: "https://forgejo.example.org", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.RepoWebURL(adapter.RepoRef{Namespace: "cs101", Name: "hw1-bob"}); got != "https://forgejo.example.org/cs101/hw1-bob" {
		t.Errorf("forgejo web URL = %q", got)
	}
	// A Gitea-constructed adapter builds the same shape.
	g, _ := NewWithHost(Config{BaseURL: "https://gitea.example.org/", Token: "t"}, adapter.HostGitea)
	if got := g.RepoWebURL(adapter.RepoRef{Namespace: "o", Name: "r"}); got != "https://gitea.example.org/o/r" {
		t.Errorf("gitea web URL = %q", got)
	}
}
