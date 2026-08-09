// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/EduCloud-Ecosystem/cairn/internal/identity"
	"github.com/EduCloud-Ecosystem/cairn/internal/store/memory"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

// newSPAServer builds a server that serves a dashboard from a temp web dir, so
// the "/" catch-all (staticSPAHandler) path is exercised rather than the inline
// status page. It also writes a static asset, to prove asset requests are not
// caught by the student redirect.
func newSPAServer(t *testing.T, admins ...string) *Server {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>dashboard</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log(1)"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := fakeResolver{username: "alice"}
	return New(Options{
		Store:       memory.New(),
		Queue:       &spyQueue{},
		Resolvers:   map[adapter.Host]identity.Resolver{r.Host(): r},
		LoginHost:   r.Host(),
		AuthEnabled: true,
		AdminUsers:  admins,
		WebDir:      dir,
	})
}

// A signed-in student who lands on "/" is sent to their own page instead of the
// instructor console — the dead end CC-CA3 Finding B describes.
func TestRootRedirectsSignedInStudentToMe(t *testing.T) {
	srv := newSPAServer(t, "alice")
	cookie := studentCookie(srv, adapter.HostGitHub, "bob")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("student at / = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/me" {
		t.Errorf("redirect Location = %q, want %q", got, "/me")
	}
}

// An operator must be unaffected: the dashboard still loads, and /auth/me still
// resolves them. This is the regression the fix is most likely to cause.
func TestRootUnaffectedForOperator(t *testing.T) {
	srv := newSPAServer(t, "alice")
	cookie := login(t, srv)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("operator at / = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "<html>dashboard</html>" {
		t.Errorf("operator did not get the dashboard shell: %q", body)
	}

	// The console's own identity call must still succeed for an operator.
	meReq := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	meReq.AddCookie(cookie)
	meRec := httptest.NewRecorder()
	srv.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Errorf("/auth/me for operator = %d, want 200", meRec.Code)
	}
}

// An unauthenticated visitor still gets the SPA shell, so App.tsx's
// operator === null branch (the sign-in card) still renders.
func TestRootUnaffectedForAnonymous(t *testing.T) {
	srv := newSPAServer(t, "alice")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous at / = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "<html>dashboard</html>" {
		t.Errorf("anonymous did not get the dashboard shell: %q", body)
	}
}

// The redirect must apply only to the exact root. staticSPAHandler is a
// catch-all that also serves assets and SPA fallback routes; redirecting those
// would break the dashboard for anyone holding a student session.
func TestStudentSessionStillLoadsStaticAssets(t *testing.T) {
	srv := newSPAServer(t, "alice")
	cookie := studentCookie(srv, adapter.HostGitHub, "bob")

	for _, path := range []string{"/assets/app.js", "/classrooms/abc"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code == http.StatusFound {
			t.Errorf("%s was redirected; only the exact root should be", path)
		}
	}
}

// The same redirect applies when no dashboard is built and "/" is the inline
// status page, which is what a fresh deployment serves.
func TestStatusPageRootRedirectsStudent(t *testing.T) {
	srv, _, _ := newTestServer("operator")
	cookie := studentCookie(srv, adapter.HostGitHub, "bob")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("student at status-page / = %d, want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/me" {
		t.Errorf("redirect Location = %q, want %q", got, "/me")
	}
}

// An expired or unknown cookie must not be treated as a student session — the
// visitor falls through to the sign-in card rather than bouncing to /me.
func TestRootUnaffectedForStaleCookie(t *testing.T) {
	srv := newSPAServer(t, "alice")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "no-such-session"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("stale cookie at / = %d, want 200 (fall through)", rec.Code)
	}
}
