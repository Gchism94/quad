// SPDX-License-Identifier: AGPL-3.0-or-later

package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

func newForgejoTestServer(t *testing.T, handler http.Handler) (*Forgejo, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	f := NewForgejo("client-id", "client-secret", "https://cairn.example/auth/callback", srv.URL)
	f.HTTPClient = srv.Client()
	return f, srv
}

// bg is a package-level background context shared across tests.
var bg = context.Background()

func TestForgejoHost(t *testing.T) {
	f := NewForgejo("id", "secret", "https://cb", "https://forgejo.example.org")
	if f.Host() != adapter.HostForgejo {
		t.Fatalf("Host() = %q, want HostForgejo", f.Host())
	}
}

// TestGiteaResolverHost confirms a resolver constructed for Gitea reports the
// gitea host (so the resolvers-map key and the stamped claim carry "gitea"),
// while the OAuth2 flow it drives is identical to Forgejo's.
func TestGiteaResolverHost(t *testing.T) {
	f := NewForgejoWithHost("id", "secret", "https://cb", "https://gitea.example.org", adapter.HostGitea)
	if f.Host() != adapter.HostGitea {
		t.Fatalf("Host() = %q, want HostGitea", f.Host())
	}

	// Same OAuth2 endpoints as Forgejo: token exchange at /access_token, user at
	// /api/v1/user. Drive a full Resolve against a test server to prove it.
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch {
		case calls == 1 && strings.HasSuffix(r.URL.Path, "/access_token"):
			json.NewEncoder(w).Encode(map[string]string{"access_token": "x"})
		case calls == 2 && strings.HasSuffix(r.URL.Path, "/api/v1/user"):
			json.NewEncoder(w).Encode(map[string]any{"login": "stud", "id": 7})
		default:
			t.Errorf("unexpected call %d: %s %s", calls, r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	g := NewForgejoWithHost("id", "secret", "https://cb", srv.URL, adapter.HostGitea)
	g.HTTPClient = srv.Client()
	username, hostUserID, err := g.Resolve(bg, "auth-code")
	if err != nil {
		t.Fatal(err)
	}
	if username != "stud" || hostUserID != "7" {
		t.Fatalf("Resolve = (%q, %q), want (stud, 7)", username, hostUserID)
	}
}

func TestForgejoAuthorizeURL(t *testing.T) {
	f := NewForgejo("my-client", "secret", "https://cairn.example/auth/callback", "https://forgejo.example.org")
	got := f.AuthorizeURL("test-state")

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()

	if !strings.HasPrefix(got, "https://forgejo.example.org/") {
		t.Errorf("AuthorizeURL = %q, should start with instance base", got)
	}
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q, want code (Gitea requirement)", q.Get("response_type"))
	}
	if q.Get("state") != "test-state" {
		t.Errorf("state = %q, want test-state", q.Get("state"))
	}
	if q.Get("client_id") != "my-client" {
		t.Errorf("client_id = %q, want my-client", q.Get("client_id"))
	}
}

func TestForgejoResolve(t *testing.T) {
	calls := 0
	f, srv := newForgejoTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case calls == 1 && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/access_token"):
			// Assert Gitea-required grant_type
			r.ParseForm()
			if r.FormValue("grant_type") != "authorization_code" {
				t.Errorf("grant_type = %q, want authorization_code", r.FormValue("grant_type"))
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"access_token": "x"})

		case calls == 2 && r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/user"):
			// OAuth2 access tokens require Bearer, not the PAT "token" scheme
			if auth := r.Header.Get("Authorization"); auth != "Bearer x" {
				t.Errorf("Authorization = %q, want \"Bearer x\"", auth)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"login": "stud", "id": 4242})

		default:
			t.Errorf("unexpected call %d: %s %s", calls, r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	username, hostUserID, err := f.Resolve(bg, "auth-code")
	if err != nil {
		t.Fatal(err)
	}
	if username != "stud" {
		t.Errorf("username = %q, want stud", username)
	}
	if hostUserID != "4242" {
		t.Errorf("hostUserID = %q, want 4242", hostUserID)
	}
	if calls != 2 {
		t.Fatalf("want 2 calls (token exchange + /user), got %d", calls)
	}
}

func TestForgejoResolveEmptyAccessToken(t *testing.T) {
	f, srv := newForgejoTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"access_token": "", "error": "bad_code"})
	}))
	defer srv.Close()

	if _, _, err := f.Resolve(bg, "bad"); err == nil {
		t.Fatal("want error for empty access_token, got nil")
	}
}

func TestForgejoResolveEmptyLogin(t *testing.T) {
	calls := 0
	f, srv := newForgejoTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
		} else {
			json.NewEncoder(w).Encode(map[string]any{"login": "", "id": 99})
		}
	}))
	defer srv.Close()

	if _, _, err := f.Resolve(bg, "code"); err == nil {
		t.Fatal("want error for empty login, got nil")
	}
}

func TestForgejoResolveZeroID(t *testing.T) {
	calls := 0
	f, srv := newForgejoTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
		} else {
			json.NewEncoder(w).Encode(map[string]any{"login": "stud", "id": 0})
		}
	}))
	defer srv.Close()

	if _, _, err := f.Resolve(bg, "code"); err == nil {
		t.Fatal("want error for zero id, got nil")
	}
}
