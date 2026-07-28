// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/quad/quad/pkg/adapter"
)

var bg = context.Background()

// assertToken checks the PRIVATE-TOKEN header carries the configured PAT.
func assertToken(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("PRIVATE-TOKEN"); got != "t" {
		t.Errorf("PRIVATE-TOKEN = %q, want t", got)
	}
}

func newTestAdapter(t *testing.T, srv *httptest.Server) *Adapter {
	t.Helper()
	a, err := New(Config{BaseURL: srv.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// --- New / Host / RepoWebURL ----------------------------------------------

func TestNewRejectsEmptyToken(t *testing.T) {
	if _, err := New(Config{BaseURL: "https://gitlab.com"}); err == nil {
		t.Fatal("want error for empty Token")
	}
}

func TestNewDefaultsBaseURL(t *testing.T) {
	a, err := New(Config{Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if a.baseURL != "https://gitlab.com" {
		t.Fatalf("baseURL = %q, want https://gitlab.com", a.baseURL)
	}
}

func TestHostIsGitLab(t *testing.T) {
	a, _ := New(Config{Token: "t"})
	if a.Host() != adapter.HostGitLab {
		t.Fatalf("Host() = %q, want gitlab", a.Host())
	}
}

func TestRepoWebURL(t *testing.T) {
	a, _ := New(Config{BaseURL: "https://gitlab.example.edu/", Token: "t"})
	got := a.RepoWebURL(adapter.RepoRef{Namespace: "cs101", Name: "hw1-alice"})
	if got != "https://gitlab.example.edu/cs101/hw1-alice" {
		t.Errorf("RepoWebURL = %q", got)
	}
}

// --- EnsureNamespace ------------------------------------------------------

func TestEnsureNamespaceExisting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertToken(t, r)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v4/groups/cs101" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":7,"path":"cs101"}`))
	}))
	defer srv.Close()
	ref, err := newTestAdapter(t, srv).EnsureNamespace(bg, "cs101")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Host != adapter.HostGitLab || ref.Slug != "cs101" {
		t.Fatalf("ref = %+v", ref)
	}
}

func TestEnsureNamespaceCreate(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertToken(t, r)
		calls++
		switch calls {
		case 1: // GET group → 404
			w.WriteHeader(http.StatusNotFound)
		case 2: // POST /groups → 201
			if r.Method != http.MethodPost || r.URL.Path != "/api/v4/groups" {
				t.Errorf("call 2: unexpected %s %s", r.Method, r.URL.Path)
			}
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["path"] != "neworg" || body["name"] != "neworg" {
				t.Errorf("body = %+v, want path/name neworg", body)
			}
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":9,"path":"neworg"}`))
		default:
			t.Errorf("unexpected call %d", calls)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	ref, err := newTestAdapter(t, srv).EnsureNamespace(bg, "neworg")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Slug != "neworg" {
		t.Fatalf("ref.Slug = %q", ref.Slug)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestEnsureNamespaceConflictConfirmed(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1: // GET → 404
			w.WriteHeader(http.StatusNotFound)
		case 2: // POST → 409 (taken)
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"message":"has already been taken"}`))
		case 3: // confirm GET → 200
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"id":3,"path":"org"}`))
		default:
			t.Errorf("unexpected call %d", calls)
		}
	}))
	defer srv.Close()
	if _, err := newTestAdapter(t, srv).EnsureNamespace(bg, "org"); err != nil {
		t.Fatalf("want success on confirmed conflict, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 (GET, POST, confirm GET)", calls)
	}
}

func TestEnsureNamespaceConflictGenuineError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1: // GET → 404
			w.WriteHeader(http.StatusNotFound)
		case 2: // POST → 400 (genuine validation error)
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"message":"path is invalid"}`))
		case 3: // confirm GET → still 404 → original error surfaces
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected call %d", calls)
		}
	}))
	defer srv.Close()
	if _, err := newTestAdapter(t, srv).EnsureNamespace(bg, "bad name"); err == nil {
		t.Fatal("want error when create fails and group still absent")
	}
}

// --- CreateRepoFromTemplate (fork) ----------------------------------------

func TestCreateRepoFromTemplateForks(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertToken(t, r)
		calls++
		switch {
		case calls == 1 && r.Method == http.MethodGet && r.URL.Path == "/api/v4/projects/cs101/hw1-alice":
			w.WriteHeader(http.StatusNotFound) // RepoExists → false
		case calls == 2 && r.Method == http.MethodPost && r.URL.Path == "/api/v4/projects/instr/hw1-template/fork":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["namespace_path"] != "cs101" || body["path"] != "hw1-alice" || body["visibility"] != "private" {
				t.Errorf("fork body = %+v", body)
			}
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":123,"path":"hw1-alice"}`))
		case calls == 3 && r.Method == http.MethodDelete && r.URL.Path == "/api/v4/projects/cs101/hw1-alice/fork":
			w.WriteHeader(http.StatusNoContent) // break fork relationship
		default:
			t.Errorf("unexpected call %d: %s %s", calls, r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	ref, err := newTestAdapter(t, srv).CreateRepoFromTemplate(bg,
		adapter.TemplateRef{Host: adapter.HostGitLab, Namespace: "instr", Name: "hw1-template"},
		adapter.NamespaceRef{Host: adapter.HostGitLab, Slug: "cs101"},
		"hw1-alice",
		adapter.CreateRepoOptions{Private: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Host != adapter.HostGitLab || ref.Namespace != "cs101" || ref.Name != "hw1-alice" {
		t.Fatalf("ref = %+v", ref)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 (exists, fork, break-fork)", calls)
	}
}

func TestCreateRepoFromTemplateIdempotent(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet {
			t.Errorf("idempotent case: unexpected %s (want only the RepoExists GET)", r.Method)
		}
		w.WriteHeader(http.StatusOK) // project already exists
		w.Write([]byte(`{"id":123,"path":"hw1-alice"}`))
	}))
	defer srv.Close()
	ref, err := newTestAdapter(t, srv).CreateRepoFromTemplate(bg,
		adapter.TemplateRef{Namespace: "instr", Name: "tmpl"},
		adapter.NamespaceRef{Slug: "cs101"},
		"hw1-alice", adapter.CreateRepoOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Name != "hw1-alice" {
		t.Fatalf("ref.Name = %q", ref.Name)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (RepoExists only)", calls)
	}
}

// --- RepoExists -----------------------------------------------------------

func TestRepoExists(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   bool
	}{{http.StatusOK, true}, {http.StatusNotFound, false}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v4/projects/cs101/hw1" {
				t.Errorf("path = %s", r.URL.Path)
			}
			w.WriteHeader(tc.status)
		}))
		got, err := newTestAdapter(t, srv).RepoExists(bg, adapter.RepoRef{Namespace: "cs101", Name: "hw1"})
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("status %d → exists %v, want %v", tc.status, got, tc.want)
		}
	}
}

// --- SetCollaborator / RemoveCollaborator ---------------------------------

func TestSetCollaboratorResolvesUserID(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertToken(t, r)
		calls++
		switch calls {
		case 1: // resolve user id
			if r.URL.Path != "/api/v4/users" || r.URL.Query().Get("username") != "bob" {
				t.Errorf("call 1: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			}
			w.Write([]byte(`[{"id":42,"username":"bob"}]`))
		case 2: // add member
			if r.Method != http.MethodPost || r.URL.Path != "/api/v4/projects/cs101/hw1/members" {
				t.Errorf("call 2: %s %s", r.Method, r.URL.Path)
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["user_id"] != float64(42) || body["access_level"] != float64(30) {
				t.Errorf("member body = %+v, want user_id 42 access_level 30", body)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected call %d", calls)
		}
	}))
	defer srv.Close()
	if err := newTestAdapter(t, srv).SetCollaborator(bg, adapter.RepoRef{Namespace: "cs101", Name: "hw1"}, "bob", adapter.RoleWrite); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

// TestSetCollaboratorMemberAddResponses covers how the member-add POST response is
// interpreted: an already-satisfied membership (already a direct member, or an
// equal-or-higher inherited level) is non-fatal, while genuine failures error.
func TestSetCollaboratorMemberAddResponses(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{"created", http.StatusCreated, ``, false},
		{"already a direct member (409)", http.StatusConflict, `{"message":"Member already exists"}`, false},
		{
			"inherited access (400)", http.StatusBadRequest,
			`{"message":{"access_level":["should be greater than or equal to Owner inherited membership from group g"]}}`,
			false,
		},
		{"unrelated 400 still errors", http.StatusBadRequest, `{"message":"bad request"}`, true},
		{"forbidden errors", http.StatusForbidden, `{"message":"403 Forbidden"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertToken(t, r)
				calls++
				switch calls {
				case 1: // resolve user id
					w.Write([]byte(`[{"id":42}]`))
				case 2: // add member → case-specific response
					if r.Method != http.MethodPost || r.URL.Path != "/api/v4/projects/cs101/hw1/members" {
						t.Errorf("call 2: %s %s", r.Method, r.URL.Path)
					}
					w.WriteHeader(tc.status)
					if tc.body != "" {
						w.Write([]byte(tc.body))
					}
				default:
					t.Errorf("unexpected call %d (no PUT/update expected)", calls)
				}
			}))
			defer srv.Close()

			err := newTestAdapter(t, srv).SetCollaborator(bg, adapter.RepoRef{Namespace: "cs101", Name: "hw1"}, "bob", adapter.RoleWrite)
			if tc.wantErr && err == nil {
				t.Errorf("status %d: want error, got nil", tc.status)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("status %d: want nil, got %v", tc.status, err)
			}
		})
	}
}

func TestRemoveCollaborator(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			w.Write([]byte(`[{"id":42}]`))
		case 2:
			if r.Method != http.MethodDelete || r.URL.Path != "/api/v4/projects/cs101/hw1/members/42" {
				t.Errorf("call 2: %s %s", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	if err := newTestAdapter(t, srv).RemoveCollaborator(bg, adapter.RepoRef{Namespace: "cs101", Name: "hw1"}, "bob"); err != nil {
		t.Fatal(err)
	}
}

// --- LatestCommit ---------------------------------------------------------

func TestLatestCommit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects/cs101/hw1/repository/commits" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Write([]byte(`[{"id":"deadbeef","message":"init","committed_date":"2026-01-02T03:04:05Z","author_name":"A"}]`))
	}))
	defer srv.Close()
	c, err := newTestAdapter(t, srv).LatestCommit(bg, adapter.RepoRef{Namespace: "cs101", Name: "hw1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.SHA != "deadbeef" || c.Message != "init" {
		t.Fatalf("commit = %+v", c)
	}
	// Privacy: we never surface the author's display name as a username.
	if c.AuthorUsername != "" {
		t.Errorf("AuthorUsername = %q, want empty", c.AuthorUsername)
	}
}

func TestLatestCommitEmptyRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c, err := newTestAdapter(t, srv).LatestCommit(bg, adapter.RepoRef{Namespace: "cs101", Name: "hw1"}, "")
	if err != nil {
		t.Fatalf("empty repo should not error: %v", err)
	}
	if c.SHA != "" {
		t.Errorf("want zero commit, got %+v", c)
	}
}

// --- Lock / Unlock --------------------------------------------------------

func TestLockRepoProtectsDefaultBranch(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertToken(t, r)
		calls++
		switch calls {
		case 1: // GET project → default_branch
			w.Write([]byte(`{"id":1,"default_branch":"main"}`))
		case 2: // DELETE existing protection (ignore 404)
			if r.Method != http.MethodDelete || r.URL.Path != "/api/v4/projects/cs101/hw1/protected_branches/main" {
				t.Errorf("call 2: %s %s", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNotFound)
		case 3: // POST protection push_access_level 0
			if r.Method != http.MethodPost || r.URL.Path != "/api/v4/projects/cs101/hw1/protected_branches" {
				t.Errorf("call 3: %s %s", r.Method, r.URL.Path)
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["name"] != "main" || body["push_access_level"] != float64(0) {
				t.Errorf("protect body = %+v, want main push_access_level 0", body)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected call %d", calls)
		}
	}))
	defer srv.Close()
	if err := newTestAdapter(t, srv).LockRepo(bg, adapter.RepoRef{Namespace: "cs101", Name: "hw1"}); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestUnlockRepoRestoresDeveloperPush(t *testing.T) {
	var protectBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			w.Write([]byte(`{"default_branch":"main"}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost:
			json.NewDecoder(r.Body).Decode(&protectBody)
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()
	if err := newTestAdapter(t, srv).UnlockRepo(bg, adapter.RepoRef{Namespace: "cs101", Name: "hw1"}); err != nil {
		t.Fatal(err)
	}
	if protectBody["push_access_level"] != float64(30) {
		t.Errorf("unlock push_access_level = %v, want 30 (developer)", protectBody["push_access_level"])
	}
}

// --- EnsureWebhook --------------------------------------------------------

func TestEnsureWebhookCreate(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertToken(t, r)
		calls++
		switch calls {
		case 1: // list hooks → none
			if r.URL.Path != "/api/v4/projects/cs101/hw1/hooks" {
				t.Errorf("call 1 path = %s", r.URL.Path)
			}
			w.Write([]byte(`[]`))
		case 2: // create hook
			if r.Method != http.MethodPost {
				t.Errorf("call 2: %s", r.Method)
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["url"] != "https://quad/webhooks/gitlab" || body["push_events"] != true || body["token"] != "shh" {
				t.Errorf("hook body = %+v", body)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected call %d", calls)
		}
	}))
	defer srv.Close()
	err := newTestAdapter(t, srv).EnsureWebhook(bg, adapter.RepoRef{Namespace: "cs101", Name: "hw1"},
		adapter.WebhookSpec{URL: "https://quad/webhooks/gitlab", Secret: "shh", Events: []string{"push"}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestEnsureWebhookUpdatesExisting(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1: // list → existing hook with same URL
			w.Write([]byte(`[{"id":55,"url":"https://quad/webhooks/gitlab"}]`))
		case 2: // PUT update
			if r.Method != http.MethodPut || r.URL.Path != "/api/v4/projects/cs101/hw1/hooks/55" {
				t.Errorf("call 2: %s %s", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected call %d", calls)
		}
	}))
	defer srv.Close()
	err := newTestAdapter(t, srv).EnsureWebhook(bg, adapter.RepoRef{Namespace: "cs101", Name: "hw1"},
		adapter.WebhookSpec{URL: "https://quad/webhooks/gitlab", Secret: "shh"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (list, PUT)", calls)
	}
}

// --- DispatchGrading / GradingResult --------------------------------------

func TestDispatchGradingNotImplemented(t *testing.T) {
	a, _ := New(Config{Token: "t"})
	if err := a.DispatchGrading(bg, adapter.GradingDispatch{}); err != adapter.ErrNotImplemented {
		t.Fatalf("DispatchGrading = %v, want ErrNotImplemented", err)
	}
}

func TestGradingResult(t *testing.T) {
	cases := []struct {
		state string
		want  adapter.CheckStatus
	}{
		{"success", adapter.CheckPassed},
		{"failed", adapter.CheckFailed},
		{"running", adapter.CheckRunning},
		{"canceled", adapter.CheckError},
		{"pending", adapter.CheckPending},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v4/projects/cs101/hw1/repository/commits/abc/statuses" {
					t.Errorf("path = %s", r.URL.Path)
				}
				w.Write([]byte(`[{"status":"` + tc.state + `","target_url":"https://ci"}]`))
			}))
			defer srv.Close()
			res, err := newTestAdapter(t, srv).GradingResult(bg, adapter.RepoRef{Namespace: "cs101", Name: "hw1"}, "abc")
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != tc.want {
				t.Errorf("status %q → %q, want %q", tc.state, res.Status, tc.want)
			}
		})
	}
}

func TestGradingResultNoStatuses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	res, err := newTestAdapter(t, srv).GradingResult(bg, adapter.RepoRef{Namespace: "cs101", Name: "hw1"}, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != adapter.CheckPending {
		t.Errorf("no statuses → %q, want pending", res.Status)
	}
}
