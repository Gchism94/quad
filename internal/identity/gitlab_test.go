// SPDX-License-Identifier: AGPL-3.0-or-later

package identity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

func TestGitLabHost(t *testing.T) {
	g := NewGitLab("id", "secret", "https://cb", "https://gitlab.example.org")
	if g.Host() != adapter.HostGitLab {
		t.Fatalf("Host() = %q, want gitlab", g.Host())
	}
}

func TestGitLabDefaultsBaseURL(t *testing.T) {
	g := NewGitLab("id", "secret", "https://cb", "")
	if g.base() != "https://gitlab.com" {
		t.Fatalf("base() = %q, want https://gitlab.com", g.base())
	}
}

func TestGitLabAuthorizeURL(t *testing.T) {
	g := NewGitLab("my-client", "secret", "https://cairn.example/auth/callback", "https://gitlab.example.org")
	got := g.AuthorizeURL("test-state")

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "https://gitlab.example.org/oauth/authorize?") {
		t.Errorf("AuthorizeURL = %q, want the /oauth/authorize endpoint", got)
	}
	q := parsed.Query()
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q, want code", q.Get("response_type"))
	}
	if q.Get("scope") != "read_user" {
		t.Errorf("scope = %q, want read_user", q.Get("scope"))
	}
	if q.Get("client_id") != "my-client" {
		t.Errorf("client_id = %q, want my-client", q.Get("client_id"))
	}
	if q.Get("state") != "test-state" {
		t.Errorf("state = %q, want test-state", q.Get("state"))
	}
}

func TestGitLabResolve(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case calls == 1 && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/oauth/token"):
			r.ParseForm()
			if r.FormValue("grant_type") != "authorization_code" {
				t.Errorf("grant_type = %q, want authorization_code", r.FormValue("grant_type"))
			}
			if r.FormValue("code") != "auth-code" {
				t.Errorf("code = %q, want auth-code", r.FormValue("code"))
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})

		case calls == 2 && r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/api/v4/user"):
			if auth := r.Header.Get("Authorization"); auth != "Bearer tok" {
				t.Errorf("Authorization = %q, want \"Bearer tok\"", auth)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"username": "stud", "id": 4242})

		default:
			t.Errorf("unexpected call %d: %s %s", calls, r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	g := NewGitLab("id", "secret", "https://cb", srv.URL)
	g.HTTPClient = srv.Client()
	username, hostUserID, err := g.Resolve(bg, "auth-code")
	if err != nil {
		t.Fatal(err)
	}
	if username != "stud" {
		t.Errorf("username = %q, want stud", username)
	}
	if hostUserID != "4242" {
		t.Errorf("hostUserID = %q, want 4242 (numeric id, the durable anchor)", hostUserID)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (token exchange + /user)", calls)
	}
}

func TestGitLabResolveEmptyToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"access_token": "", "error": "invalid_grant"})
	}))
	defer srv.Close()
	g := NewGitLab("id", "secret", "https://cb", srv.URL)
	g.HTTPClient = srv.Client()
	if _, _, err := g.Resolve(bg, "bad"); err == nil {
		t.Fatal("want error for empty access_token")
	}
}

func TestGitLabResolveEmptyUsername(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
		} else {
			json.NewEncoder(w).Encode(map[string]any{"username": "", "id": 7})
		}
	}))
	defer srv.Close()
	g := NewGitLab("id", "secret", "https://cb", srv.URL)
	g.HTTPClient = srv.Client()
	if _, _, err := g.Resolve(bg, "code"); err == nil {
		t.Fatal("want error for empty username")
	}
}
