// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/identity"
	"github.com/EduCloud-Ecosystem/cairn/internal/provisioning"
	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/internal/store/memory"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

// fakeResolver returns a fixed username and hostUserID without any network call.
// hostUserID defaults to "100" when unset; host defaults to HostGitHub when unset.
type fakeResolver struct {
	username   string
	hostUserID string
	host       adapter.Host // defaults to HostGitHub
}

func (f fakeResolver) Host() adapter.Host {
	if f.host != "" {
		return f.host
	}
	return adapter.HostGitHub
}
func (f fakeResolver) AuthorizeURL(state string) string {
	return "https://auth.example/login?state=" + state
}
func (f fakeResolver) Resolve(context.Context, string) (string, string, error) {
	hid := f.hostUserID
	if hid == "" {
		hid = "100"
	}
	return f.username, hid, nil
}

var _ identity.Resolver = fakeResolver{}

// spyQueue records Enqueue calls.
type spyQueue struct {
	jobs []struct {
		Type   provisioning.JobType
		Target string
		Idem   string
	}
}

func (q *spyQueue) Enqueue(_ context.Context, t provisioning.JobType, target, idem string) error {
	q.jobs = append(q.jobs, struct {
		Type   provisioning.JobType
		Target string
		Idem   string
	}{t, target, idem})
	return nil
}

var _ provisioning.Queue = (*spyQueue)(nil)

func newTestServer(username string) (*Server, *memory.Store, *spyQueue) {
	st := memory.New()
	q := &spyQueue{}
	r := fakeResolver{username: username}
	srv := New(Options{
		Store:            st,
		Queue:            q,
		Resolvers:        map[adapter.Host]identity.Resolver{r.Host(): r},
		Adapters:         map[adapter.Host]adapter.Adapter{r.Host(): &fakeWorkerAdapter{}},
		LoginHost:        r.Host(),
		GraderConfigured: true, // enabled by default so grade-endpoint tests work
	})
	return srv, st, q
}

func TestCreateClassroom(t *testing.T) {
	srv, st, _ := newTestServer("alice")
	body := `{"name":"CS101","host":"github","host_namespace":"cs101-org"}`
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/classrooms", strings.NewReader(body)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var out store.Classroom
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID == "" {
		t.Fatal("expected an ID")
	}
	if _, err := st.GetClassroom(context.Background(), out.ID); err != nil {
		t.Fatalf("classroom not persisted: %v", err)
	}
}

func TestAcceptThenCallbackSelfClaim(t *testing.T) {
	srv, st, q := newTestServer("alice")
	ctx := context.Background()

	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c1", Name: "CS101", Host: adapter.HostGitHub, HostNamespace: "cs101-org"})
	_ = st.CreateAssignment(ctx, &store.Assignment{
		ID: "a1", ClassroomID: "c1", Slug: "hw1",
		TemplateRef: adapter.TemplateRef{Host: adapter.HostGitHub, Namespace: "cs101-org", Name: "hw1-template"},
	})

	// 1) accept -> 302 redirect carrying a state parameter.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assignments/a1/accept", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("accept status = %d, want 302", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("expected a state in the redirect URL")
	}

	// 2) callback -> binds the username, enqueues provisioning, and (browser-driven)
	// redirects the student to their own-work page with a session cookie.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/callback?code=xyz&state="+state, nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/me" {
		t.Fatalf("callback redirect = %q, want /me", loc)
	}

	re, err := st.FindRosterEntryByUsername(ctx, "c1", "alice")
	if err != nil {
		t.Fatalf("roster entry not created: %v", err)
	}
	if re.Status != store.RosterActive {
		t.Fatalf("roster status = %q, want active", re.Status)
	}
	sub, err := st.FindSubmission(ctx, "a1", re.ID)
	if err != nil {
		t.Fatalf("submission not created: %v", err)
	}
	if len(q.jobs) != 1 || q.jobs[0].Type != provisioning.JobCreateRepo || q.jobs[0].Target != sub.ID {
		t.Fatalf("expected one create_repo job for submission %s, got %+v", sub.ID, q.jobs)
	}
}

func TestSetDeadline(t *testing.T) {
	srv, st, _ := newTestServer("alice")
	ctx := context.Background()
	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c1", Host: adapter.HostGitHub, HostNamespace: "org"})
	_ = st.CreateAssignment(ctx, &store.Assignment{ID: "a1", ClassroomID: "c1", Slug: "hw1"})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/assignments/a1/deadline", strings.NewReader(`{"deadline":"2025-12-01T23:59:00Z"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("set status = %d; body=%s", rec.Code, rec.Body.String())
	}
	a, _ := st.GetAssignment(ctx, "a1")
	if a.Deadline == nil || !a.Deadline.Equal(time.Date(2025, 12, 1, 23, 59, 0, 0, time.UTC)) {
		t.Fatalf("deadline = %v", a.Deadline)
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/assignments/a1/deadline", strings.NewReader(`{"deadline":null}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d", rec.Code)
	}
	if a, _ := st.GetAssignment(ctx, "a1"); a.Deadline != nil {
		t.Fatalf("expected nil deadline, got %v", a.Deadline)
	}
}

func TestLockEndpoint(t *testing.T) {
	srv, st, q := newTestServer("alice")
	ctx := context.Background()
	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c1", Host: adapter.HostGitHub, HostNamespace: "org"})
	_ = st.CreateAssignment(ctx, &store.Assignment{ID: "a1", ClassroomID: "c1", Slug: "hw1"})
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1", Repo: adapter.RepoRef{Host: adapter.HostGitHub, Namespace: "org", Name: "hw1-bob"}})
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s2", AssignmentID: "a1"}) // no repo

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/assignments/a1/lock", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("lock status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if len(q.jobs) != 1 || q.jobs[0].Type != provisioning.JobLockRepo || q.jobs[0].Target != "s1" {
		t.Fatalf("expected one lock job for s1, got %+v", q.jobs)
	}
}

func TestGradesCSV(t *testing.T) {
	srv, st, _ := newTestServer("alice")
	ctx := context.Background()

	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c1", Host: adapter.HostGitHub, HostNamespace: "org"})
	_ = st.CreateAssignment(ctx, &store.Assignment{ID: "a1", ClassroomID: "c1", Slug: "hw1"})
	_ = st.CreateRosterEntry(ctx, &store.RosterEntry{ID: "r1", ClassroomID: "c1", HostUsername: "alice", Status: store.RosterActive})
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1", RosterEntryID: "r1", Status: "active"})
	_ = st.CreateGrade(ctx, &store.Grade{ID: "g1", SubmissionID: "s1", Score: 9, MaxScore: 10, GradedAt: time.Now()})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/classrooms/c1/grades.csv", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"username,assignment,score,max_score", "alice", "hw1", "9", "10"} {
		if !strings.Contains(body, want) {
			t.Fatalf("CSV missing %q; got:\n%s", want, body)
		}
	}
}

func TestGradeEndpoint(t *testing.T) {
	srv, st, q := newTestServer("alice")
	ctx := context.Background()
	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c1", Host: adapter.HostGitHub, HostNamespace: "org"})
	_ = st.CreateAssignment(ctx, &store.Assignment{ID: "a1", ClassroomID: "c1", Slug: "hw1"})
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1", Repo: adapter.RepoRef{Host: adapter.HostGitHub, Namespace: "org", Name: "hw1-bob"}})
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s2", AssignmentID: "a1"}) // no repo

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/assignments/a1/grade", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("grade status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if len(q.jobs) != 1 || q.jobs[0].Type != provisioning.JobGrade || q.jobs[0].Target != "s1" {
		t.Fatalf("expected one grade job for s1, got %+v", q.jobs)
	}
}

func TestListClassrooms(t *testing.T) {
	srv, st, _ := newTestServer("alice")
	ctx := context.Background()
	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c1", Name: "CS101", Host: adapter.HostGitHub, HostNamespace: "org1"})
	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c2", Name: "CS201", Host: adapter.HostGitHub, HostNamespace: "org2"})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/classrooms", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out []store.Classroom
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d classrooms, want 2", len(out))
	}
}

func TestListSubmissionsView(t *testing.T) {
	srv, st, _ := newTestServer("alice")
	ctx := context.Background()
	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c1", Host: adapter.HostGitHub, HostNamespace: "org"})
	_ = st.CreateAssignment(ctx, &store.Assignment{ID: "a1", ClassroomID: "c1", Slug: "hw1"})
	_ = st.CreateRosterEntry(ctx, &store.RosterEntry{ID: "r1", ClassroomID: "c1", HostUsername: "alice", Status: store.RosterActive})
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1", RosterEntryID: "r1", Status: "active", Repo: adapter.RepoRef{Host: adapter.HostGitHub, Namespace: "org", Name: "hw1-alice"}})
	_ = st.CreateGrade(ctx, &store.Grade{ID: "g1", SubmissionID: "s1", Score: 8, MaxScore: 10, GradedAt: time.Now()})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assignments/a1/submissions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d submissions, want 1", len(out))
	}
	if out[0]["username"] != "alice" || out[0]["score"] != float64(8) || out[0]["status"] != "active" {
		t.Fatalf("unexpected view: %+v", out[0])
	}
}

func TestWebDirServesSPAAndYieldsToAPI(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><div id=root></div>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := New(Options{
		Store:     memory.New(),
		Queue:     &spyQueue{},
		Resolvers: map[adapter.Host]identity.Resolver{adapter.HostGitHub: fakeResolver{username: "alice"}},
		LoginHost: adapter.HostGitHub,
		WebDir:    dir,
	})

	get := func(path string) (int, string, string) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String(), rec.Header().Get("Content-Type")
	}

	// Root serves index.html.
	if code, body, _ := get("/"); code != 200 || !strings.Contains(body, "id=root") {
		t.Fatalf("GET / = %d %q, want index.html", code, body)
	}
	// Real asset is served from disk.
	if code, body, _ := get("/assets/app.js"); code != 200 || !strings.Contains(body, "console.log") {
		t.Fatalf("GET /assets/app.js = %d %q, want the asset", code, body)
	}
	// Unknown path falls back to index.html (SPA routing).
	if code, body, _ := get("/anything/deep"); code != 200 || !strings.Contains(body, "id=root") {
		t.Fatalf("SPA fallback = %d %q, want index.html", code, body)
	}
	// API routes still win over the catch-all.
	if code, _, ct := get("/healthz"); code != 200 || !strings.Contains(ct, "application/json") {
		t.Fatalf("GET /healthz = %d ct=%q, want JSON (API precedence)", code, ct)
	}
}

// newAuthServer builds an auth-enabled server with the given admin allowlist.
func newAuthServer(admins ...string) (*Server, *memory.Store) {
	st := memory.New()
	r := fakeResolver{username: "alice"}
	srv := New(Options{
		Store:       st,
		Queue:       &spyQueue{},
		Resolvers:   map[adapter.Host]identity.Resolver{r.Host(): r},
		LoginHost:   r.Host(),
		AuthEnabled: true,
		AdminUsers:  admins,
	})
	return srv, st
}

// login drives the OAuth login flow and returns the resulting session cookie.
func login(t *testing.T, srv *Server) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("login redirect = %d, want 302", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("no state in authorize redirect")
	}
	cb := httptest.NewRecorder()
	srv.ServeHTTP(cb, httptest.NewRequest(http.MethodGet, "/auth/callback?code=x&state="+state, nil))
	if cb.Code != http.StatusFound {
		t.Fatalf("callback = %d, want 302; body=%s", cb.Code, cb.Body.String())
	}
	for _, c := range cb.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	t.Fatal("no session cookie set after login")
	return nil
}

func TestAuthProtectsManagementEndpoints(t *testing.T) {
	srv, _ := newAuthServer("alice")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/classrooms", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /classrooms = %d, want 401", rec.Code)
	}
	// healthz stays public.
	h := httptest.NewRecorder()
	srv.ServeHTTP(h, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if h.Code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", h.Code)
	}
}

func TestOperatorLoginThenAccess(t *testing.T) {
	srv, st := newAuthServer("alice")
	cookie := login(t, srv)

	// Authenticated request succeeds.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/classrooms", nil)
	req.AddCookie(cookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated GET /classrooms = %d, want 200", rec.Code)
	}

	// /auth/me reflects the operator.
	me := httptest.NewRecorder()
	mreq := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	mreq.AddCookie(cookie)
	srv.ServeHTTP(me, mreq)
	var who map[string]string
	_ = json.Unmarshal(me.Body.Bytes(), &who)
	if who["username"] != "alice" {
		t.Fatalf("/auth/me username = %q, want alice", who["username"])
	}

	// created_by is the real operator's user id, and that user exists.
	cr := httptest.NewRecorder()
	creq := httptest.NewRequest(http.MethodPost, "/classrooms", strings.NewReader(`{"name":"CS","host":"github","host_namespace":"org"}`))
	creq.AddCookie(cookie)
	srv.ServeHTTP(cr, creq)
	if cr.Code != http.StatusCreated {
		t.Fatalf("create classroom = %d, want 201", cr.Code)
	}
	var c store.Classroom
	_ = json.Unmarshal(cr.Body.Bytes(), &c)
	if c.CreatedBy == "" {
		t.Fatal("created_by should be the authenticated operator id")
	}
	if _, err := st.GetUser(context.Background(), c.CreatedBy); err != nil {
		t.Fatalf("operator user not persisted: %v", err)
	}
}

func TestLoginRejectsNonAdmin(t *testing.T) {
	srv, _ := newAuthServer("bob") // resolver returns "alice"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	cb := httptest.NewRecorder()
	srv.ServeHTTP(cb, httptest.NewRequest(http.MethodGet, "/auth/callback?code=x&state="+loc.Query().Get("state"), nil))
	if cb.Code != http.StatusForbidden {
		t.Fatalf("non-admin login = %d, want 403", cb.Code)
	}
	if len(cb.Result().Cookies()) != 0 {
		t.Fatal("no session cookie should be set for a rejected login")
	}
}

func TestLogoutEndsSession(t *testing.T) {
	srv, _ := newAuthServer("alice")
	cookie := login(t, srv)

	out := httptest.NewRecorder()
	oreq := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	oreq.AddCookie(cookie)
	srv.ServeHTTP(out, oreq)
	if out.Code != http.StatusOK {
		t.Fatalf("logout = %d, want 200", out.Code)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/classrooms", nil)
	req.AddCookie(cookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("after logout GET /classrooms = %d, want 401", rec.Code)
	}
}

// TestOversizedBodyRejected (M3): a request body larger than maxRequestBody must be
// rejected with 413 before decoding — not read entirely into memory.
func TestOversizedBodyRejected(t *testing.T) {
	srv, _, _ := newTestServer("alice") // auth disabled — goes straight to readJSON
	// Use a valid JSON prefix long enough to cross the 1 MiB limit mid-value so
	// the JSON decoder hits MaxBytesReader before any syntax error.
	bigBody := `{"name":"` + strings.Repeat("x", maxRequestBody) + `"}`
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/classrooms", strings.NewReader(bigBody)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d, want 413", rec.Code)
	}
}

// TestExpiredSessionIsRejected (M2): a session whose created timestamp is older than
// sessionTTL must be rejected and immediately removed from the sessions map.
func TestExpiredSessionIsRejected(t *testing.T) {
	srv, _ := newAuthServer("alice")
	cookie := login(t, srv)

	// Backdate the session entry to simulate expiry.
	srv.sessMu.Lock()
	if sess, ok := srv.sessions[cookie.Value]; ok {
		sess.created = time.Now().Add(-(sessionTTL + time.Minute))
		srv.sessions[cookie.Value] = sess
	}
	srv.sessMu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/classrooms", nil)
	req.AddCookie(cookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired session = %d, want 401", rec.Code)
	}

	// The expired entry must have been removed.
	srv.sessMu.Lock()
	_, still := srv.sessions[cookie.Value]
	srv.sessMu.Unlock()
	if still {
		t.Fatal("expired session was not deleted from sessions map")
	}
}

// TestExpiredStateIsRejected (M1): an OAuth state older than authFlowTTL must be
// rejected by handleCallback even if the state key exists in the map.
func TestExpiredStateIsRejected(t *testing.T) {
	srv, _ := newAuthServer("alice")

	// Insert a state that is already past its TTL.
	expiredKey := "test-expired-state"
	srv.stMu.Lock()
	srv.states[expiredKey] = authFlow{
		kind:    "login",
		created: time.Now().Add(-(authFlowTTL + time.Minute)),
	}
	srv.stMu.Unlock()

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/callback?code=x&state="+expiredKey, nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expired state callback = %d, want 400", rec.Code)
	}

	// The expired entry must have been removed (delete always fires on found states).
	srv.stMu.Lock()
	_, still := srv.states[expiredKey]
	srv.stMu.Unlock()
	if still {
		t.Fatal("expired state was not deleted from states map")
	}
}

// TestPruneStatesLockedRemovesOldKeepsNew (M1): pruneStatesLocked must delete entries
// older than authFlowTTL and leave fresh entries intact.
func TestPruneStatesLockedRemovesOldKeepsNew(t *testing.T) {
	srv, _ := newAuthServer("alice")
	now := time.Now()

	srv.stMu.Lock()
	srv.states["old"] = authFlow{kind: "login", created: now.Add(-(authFlowTTL + time.Second))}
	srv.states["fresh"] = authFlow{kind: "login", created: now}
	srv.pruneStatesLocked(now)
	srv.stMu.Unlock()

	srv.stMu.Lock()
	_, hasOld := srv.states["old"]
	_, hasFresh := srv.states["fresh"]
	srv.stMu.Unlock()

	if hasOld {
		t.Error("pruneStatesLocked: old entry was not removed")
	}
	if !hasFresh {
		t.Error("pruneStatesLocked: fresh entry was incorrectly removed")
	}
}

// newServerWithStore builds an auth-enabled server wired to an existing store, so
// two servers can share the same memory store to test cross-login scenarios.
func newServerWithStore(st *memory.Store, resolver identity.Resolver, admins ...string) *Server {
	return New(Options{
		Store:       st,
		Queue:       &spyQueue{},
		Resolvers:   map[adapter.Host]identity.Resolver{resolver.Host(): resolver},
		LoginHost:   resolver.Host(),
		AuthEnabled: true,
		AdminUsers:  admins,
	})
}

// TestOperatorRenameKeepsSameUser (H2): logging in with the same numeric host user id
// but a different username (a rename) must reuse the existing user row.
func TestOperatorRenameKeepsSameUser(t *testing.T) {
	st := memory.New()

	// First login: alice / numeric id 100.
	srv1 := newServerWithStore(st, fakeResolver{username: "alice", hostUserID: "100"}, "alice", "alice2")
	login(t, srv1)

	// Read the user row that was created.
	ctx := context.Background()
	u1, err := st.FindUserByHostUserID(ctx, adapter.HostGitHub, "100")
	if err != nil {
		t.Fatalf("user not created after first login: %v", err)
	}
	firstUserID := u1.ID

	// Second login: alice2 / numeric id 100 — same person, renamed on GitHub.
	srv2 := newServerWithStore(st, fakeResolver{username: "alice2", hostUserID: "100"}, "alice", "alice2")
	login(t, srv2)

	u2, err := st.FindUserByHostUserID(ctx, adapter.HostGitHub, "100")
	if err != nil {
		t.Fatalf("user not found after renamed login: %v", err)
	}
	if u2.ID != firstUserID {
		t.Fatalf("rename created a new user row: first=%s second=%s", firstUserID, u2.ID)
	}
}

// TestRecycledUsernameGetsNewIdentity (H2): logging in as username "alice" with a
// different numeric host user id must produce a distinct user row, not reuse the one
// from the original alice.
func TestRecycledUsernameGetsNewIdentity(t *testing.T) {
	st := memory.New()
	ctx := context.Background()

	// First: alice / id 100.
	srv1 := newServerWithStore(st, fakeResolver{username: "alice", hostUserID: "100"}, "alice")
	login(t, srv1)
	u1, err := st.FindUserByHostUserID(ctx, adapter.HostGitHub, "100")
	if err != nil {
		t.Fatalf("user 1 not found: %v", err)
	}

	// Second: alice / id 200 — the original alice deleted their account and someone
	// else claimed the username.
	srv2 := newServerWithStore(st, fakeResolver{username: "alice", hostUserID: "200"}, "alice")
	login(t, srv2)
	u2, err := st.FindUserByHostUserID(ctx, adapter.HostGitHub, "200")
	if err != nil {
		t.Fatalf("user 2 not found: %v", err)
	}
	if u2.ID == u1.ID {
		t.Fatal("recycled username reused the original user row; expected a distinct identity")
	}
}

// TestMultiHostClaimUsesCorrectResolver verifies that when a classroom's host is
// HostForgejo, the /accept → /callback flow uses the Forgejo resolver (not the
// GitHub one), and the resulting roster entry carries the Forgejo username.
func TestMultiHostClaimUsesCorrectResolver(t *testing.T) {
	ctx := context.Background()

	ghResolver := fakeResolver{username: "gh-student", hostUserID: "1"}
	fgResolver := fakeResolver{username: "fg-student", hostUserID: "2", host: adapter.HostForgejo}

	st := memory.New()
	q := &spyQueue{}
	srv := New(Options{
		Store: st,
		Queue: q,
		Resolvers: map[adapter.Host]identity.Resolver{
			adapter.HostGitHub:  ghResolver,
			adapter.HostForgejo: fgResolver,
		},
		LoginHost: adapter.HostGitHub,
	})

	_ = st.CreateClassroom(ctx, &store.Classroom{
		ID: "c1", Name: "CS101", Host: adapter.HostForgejo, HostNamespace: "cs101",
	})
	_ = st.CreateAssignment(ctx, &store.Assignment{
		ID: "a1", ClassroomID: "c1", Slug: "hw1",
		TemplateRef: adapter.TemplateRef{Host: adapter.HostForgejo, Namespace: "cs101", Name: "hw1-tmpl"},
	})

	// Step 1: /accept → 302 with a state parameter.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assignments/a1/accept", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("accept = %d, want 302", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("no state in accept redirect")
	}

	// Step 2: /callback with the state → should use the Forgejo resolver, then
	// redirect the student to /me.
	cb := httptest.NewRecorder()
	srv.ServeHTTP(cb, httptest.NewRequest(http.MethodGet, "/auth/callback?code=x&state="+state, nil))
	if cb.Code != http.StatusFound {
		t.Fatalf("callback = %d, want 302; body=%s", cb.Code, cb.Body.String())
	}

	// The Forgejo resolver returned "fg-student" — that username should be bound.
	re, err := st.FindRosterEntryByUsername(ctx, "c1", "fg-student")
	if err != nil {
		t.Fatalf("roster entry not created for fg-student: %v", err)
	}
	if re.HostUsername != "fg-student" {
		t.Fatalf("roster username = %q, want fg-student", re.HostUsername)
	}

	// The GitHub resolver must NOT have been invoked.
	if _, err := st.FindRosterEntryByUsername(ctx, "c1", "gh-student"); err == nil {
		t.Fatal("gh-student was enrolled; GitHub resolver must not be used for a Forgejo classroom")
	}

	// Exactly one provisioning job should have been enqueued.
	if len(q.jobs) != 1 || q.jobs[0].Type != provisioning.JobCreateRepo {
		t.Fatalf("expected one create_repo job, got %+v", q.jobs)
	}
}

// TestStatusPageNoWebDir asserts that GET / returns a non-empty HTML status page
// when no web dir is configured, and that other unknown paths are 404.
func TestStatusPageNoWebDir(t *testing.T) {
	st := memory.New()
	q := &spyQueue{}
	r := fakeResolver{username: "alice"}
	srv := New(Options{
		Store:     st,
		Queue:     q,
		Resolvers: map[adapter.Host]identity.Resolver{r.Host(): r},
		LoginHost: r.Host(),
		// WebDir intentionally not set
	})

	// GET / → status page
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "Cairn is running") {
		t.Error("status page body should contain 'Cairn is running'")
	}

	// GET /nonexistent → 404 (status page should NOT catch all paths)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nonexistent", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /nonexistent status = %d, want 404", rec.Code)
	}
}

// TestCreateClassroomInvalidNamespace checks that placeholder or invalid
// host_namespace values are rejected at the API boundary with 400.
func TestCreateClassroomInvalidNamespace(t *testing.T) {
	srv, _, _ := newTestServer("alice")
	cases := []struct {
		body string
		want int
	}{
		{`{"name":"CS101","host":"github","host_namespace":"<operator-username>"}`, http.StatusBadRequest},
		{`{"name":"CS101","host":"github","host_namespace":"-leading"}`, http.StatusBadRequest},
		{`{"name":"CS101","host":"github","host_namespace":"valid-org"}`, http.StatusCreated},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/classrooms", strings.NewReader(tc.body)))
		if rec.Code != tc.want {
			t.Errorf("body=%s → status %d, want %d; resp=%s", tc.body, rec.Code, tc.want, rec.Body.String())
		}
	}
}

// TestCreateClassroomUnknownHost checks that an unrecognized host value is
// rejected with 400 listing the valid hosts.
func TestCreateClassroomUnknownHost(t *testing.T) {
	srv, _, _ := newTestServer("alice")
	body := `{"name":"CS101","host":"gitlab","host_namespace":"org"}`
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/classrooms", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown host → status %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "valid hosts") {
		t.Errorf("response should name the valid hosts: %s", rec.Body.String())
	}
}

// TestGradeNoGraderConfigured asserts that POST /grade returns 409 when no
// grader has been wired up, instead of enqueueing jobs that would instantly fail.
func TestGradeNoGraderConfigured(t *testing.T) {
	st := memory.New()
	q := &spyQueue{}
	r := fakeResolver{username: "alice"}
	srv := New(Options{
		Store:            st,
		Queue:            q,
		Resolvers:        map[adapter.Host]identity.Resolver{r.Host(): r},
		LoginHost:        r.Host(),
		GraderConfigured: false, // no grader
	})
	ctx := context.Background()
	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c1", Host: adapter.HostGitHub, HostNamespace: "org"})
	_ = st.CreateAssignment(ctx, &store.Assignment{ID: "a1", ClassroomID: "c1", Slug: "hw1"})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/assignments/a1/grade", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 when no grader, got %d; body=%s", rec.Code, rec.Body.String())
	}
	if len(q.jobs) != 0 {
		t.Fatalf("want no jobs enqueued, got %d", len(q.jobs))
	}
}

// TestGradeZeroProvisionedRepos asserts the response when all submissions lack
// a provisioned repo: jobs_enqueued=0, status="no_eligible_submissions",
// skipped_unprovisioned reports the actual count.
func TestGradeZeroProvisionedRepos(t *testing.T) {
	srv, st, q := newTestServer("alice")
	ctx := context.Background()
	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c1", Host: adapter.HostGitHub, HostNamespace: "org"})
	_ = st.CreateAssignment(ctx, &store.Assignment{ID: "a1", ClassroomID: "c1", Slug: "hw1"})
	// Two submissions with no provisioned repo.
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1"})
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s2", AssignmentID: "a1", RosterEntryID: "r2"})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/assignments/a1/grade", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "no_eligible_submissions" {
		t.Errorf("status = %q, want no_eligible_submissions", out["status"])
	}
	if out["jobs_enqueued"].(float64) != 0 {
		t.Errorf("jobs_enqueued = %v, want 0", out["jobs_enqueued"])
	}
	if out["skipped_unprovisioned"].(float64) != 2 {
		t.Errorf("skipped_unprovisioned = %v, want 2", out["skipped_unprovisioned"])
	}
	if len(q.jobs) != 0 {
		t.Fatalf("want no jobs enqueued, got %d", len(q.jobs))
	}
}

// TestWorkerSubmissionTerminalFailure asserts that after MaxAttempts=1 the
// submission transitions to status="failed" with a non-empty LastError, and
// that a subsequent successful run clears both.
func TestWorkerSubmissionTerminalFailure(t *testing.T) {
	ctx := context.Background()
	st := memory.New()

	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c1", Host: adapter.HostGitHub, HostNamespace: "org"})
	_ = st.CreateAssignment(ctx, &store.Assignment{
		ID: "a1", ClassroomID: "c1", Slug: "hw1",
		TemplateRef: adapter.TemplateRef{Host: adapter.HostGitHub, Namespace: "org", Name: "tmpl"},
	})
	_ = st.CreateRosterEntry(ctx, &store.RosterEntry{ID: "r1", ClassroomID: "c1", Host: adapter.HostGitHub, HostUsername: "bob"})
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1", RosterEntryID: "r1", Status: "provisioning"})

	queue := provisioning.NewService(st)
	_ = queue.Enqueue(ctx, provisioning.JobCreateRepo, "s1", "repo:s1")

	// No adapter → EnsureNamespace → error.
	w := &provisioning.Worker{Store: st, Adapters: map[adapter.Host]adapter.Adapter{}, MaxAttempts: 1}
	did, err := w.RunOnce(ctx)
	if !did || err == nil {
		t.Fatalf("RunOnce: did=%v err=%v, want did=true and non-nil error", did, err)
	}

	sub, _ := st.GetSubmission(ctx, "s1")
	if sub.Status != "failed" {
		t.Errorf("submission status = %q, want failed", sub.Status)
	}
	if sub.LastError == "" {
		t.Error("submission LastError should be non-empty after terminal failure")
	}

	// Now wire a working adapter; a re-enqueue and successful run should clear.
	fa := &fakeWorkerAdapter{}
	w.Adapters = map[adapter.Host]adapter.Adapter{adapter.HostGitHub: fa}
	_ = queue.Enqueue(ctx, provisioning.JobCreateRepo, "s1", "repo:s1:retry")
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	sub, _ = st.GetSubmission(ctx, "s1")
	if sub.Status != "active" {
		t.Errorf("submission status after success = %q, want active", sub.Status)
	}
	if sub.LastError != "" {
		t.Errorf("submission LastError = %q, want empty after success", sub.LastError)
	}
}

// fakeWorkerAdapter is a minimal adapter for the failure/recovery test above.
type fakeWorkerAdapter struct{}

func (f *fakeWorkerAdapter) Host() adapter.Host { return adapter.HostGitHub }
func (f *fakeWorkerAdapter) RepoWebURL(repo adapter.RepoRef) string {
	return "https://github.test/" + repo.Namespace + "/" + repo.Name
}
func (f *fakeWorkerAdapter) EnsureNamespace(_ context.Context, slug string) (adapter.NamespaceRef, error) {
	return adapter.NamespaceRef{Host: adapter.HostGitHub, Slug: slug}, nil
}
func (f *fakeWorkerAdapter) CreateRepoFromTemplate(_ context.Context, _ adapter.TemplateRef, ns adapter.NamespaceRef, name string, _ adapter.CreateRepoOptions) (adapter.RepoRef, error) {
	return adapter.RepoRef{Host: adapter.HostGitHub, Namespace: ns.Slug, Name: name}, nil
}
func (f *fakeWorkerAdapter) RepoExists(context.Context, adapter.RepoRef) (bool, error) {
	return false, nil
}
func (f *fakeWorkerAdapter) SetCollaborator(context.Context, adapter.RepoRef, string, adapter.Role) error {
	return nil
}
func (f *fakeWorkerAdapter) RemoveCollaborator(context.Context, adapter.RepoRef, string) error {
	return nil
}
func (f *fakeWorkerAdapter) LatestCommit(context.Context, adapter.RepoRef, string) (adapter.Commit, error) {
	return adapter.Commit{}, nil
}
func (f *fakeWorkerAdapter) LockRepo(context.Context, adapter.RepoRef) error   { return nil }
func (f *fakeWorkerAdapter) UnlockRepo(context.Context, adapter.RepoRef) error { return nil }
func (f *fakeWorkerAdapter) EnsureWebhook(context.Context, adapter.RepoRef, adapter.WebhookSpec) error {
	return nil
}
func (f *fakeWorkerAdapter) DispatchGrading(context.Context, adapter.GradingDispatch) error {
	return nil
}
func (f *fakeWorkerAdapter) GradingResult(context.Context, adapter.RepoRef, string) (adapter.CheckResult, error) {
	return adapter.CheckResult{}, nil
}

// TestCreateClassroomInvalidJoinPolicy verifies that an unknown join_policy value
// returns 400.
func TestCreateClassroomInvalidJoinPolicy(t *testing.T) {
	srv, _, _ := newTestServer("alice")
	body := `{"name":"CS101","host":"github","host_namespace":"cs101-org","join_policy":"unknown"}`
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/classrooms", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// doAcceptCallback drives the full accept → callback flow for a given assignment
// and returns the final response.
func doAcceptCallback(srv *Server, assignmentID string) *httptest.ResponseRecorder {
	// Step 1: accept → redirect to auth URL carrying state.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assignments/"+assignmentID+"/accept", nil))
	if rec.Code != http.StatusFound {
		return rec
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		return rec
	}
	state := loc.Query().Get("state")

	// Step 2: callback → completeStudentClaim.
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/auth/callback?code=xyz&state="+state, nil))
	return rec2
}

// TestRosterPolicyBlocksUnknownStudent asserts that a classroom with join_policy=roster
// returns 403 for a username that is not pre-listed on the roster.
func TestRosterPolicyBlocksUnknownStudent(t *testing.T) {
	srv, st, q := newTestServer("unknown-student")
	ctx := context.Background()

	_ = st.CreateClassroom(ctx, &store.Classroom{
		ID:            "c1",
		Name:          "CS101",
		Host:          adapter.HostGitHub,
		HostNamespace: "cs101-org",
		JoinPolicy:    store.ClassroomJoinPolicyRoster,
	})
	_ = st.CreateAssignment(ctx, &store.Assignment{
		ID: "a1", ClassroomID: "c1", Slug: "hw1",
		TemplateRef: adapter.TemplateRef{Host: adapter.HostGitHub, Namespace: "cs101-org", Name: "hw1-template"},
	})

	rec := doAcceptCallback(srv, "a1")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if out["error"] != "not on roster" {
		t.Errorf("error = %q, want %q", out["error"], "not on roster")
	}
	if out["username"] != "unknown-student" {
		t.Errorf("username = %q, want %q", out["username"], "unknown-student")
	}
	if len(q.jobs) != 0 {
		t.Errorf("want no jobs enqueued, got %d", len(q.jobs))
	}
}

// TestRosterPolicyAllowsListedStudent asserts that a classroom with join_policy=roster
// allows a username that IS pre-listed on the roster.
func TestRosterPolicyAllowsListedStudent(t *testing.T) {
	srv, st, q := newTestServer("alice")
	ctx := context.Background()

	_ = st.CreateClassroom(ctx, &store.Classroom{
		ID:            "c1",
		Name:          "CS101",
		Host:          adapter.HostGitHub,
		HostNamespace: "cs101-org",
		JoinPolicy:    store.ClassroomJoinPolicyRoster,
	})
	_ = st.CreateAssignment(ctx, &store.Assignment{
		ID: "a1", ClassroomID: "c1", Slug: "hw1",
		TemplateRef: adapter.TemplateRef{Host: adapter.HostGitHub, Namespace: "cs101-org", Name: "hw1-template"},
	})
	// Pre-list alice on the roster.
	_ = st.CreateRosterEntry(ctx, &store.RosterEntry{
		ID:           "r1",
		ClassroomID:  "c1",
		Host:         adapter.HostGitHub,
		HostUsername: "alice",
		Status:       store.RosterInvited,
	})

	rec := doAcceptCallback(srv, "a1")
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (redirect to /me); body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/me" {
		t.Fatalf("redirect = %q, want /me", loc)
	}
	if len(q.jobs) == 0 {
		t.Error("want at least one provisioning job enqueued")
	}
	// Roster entry should now be active.
	re, err := st.FindRosterEntryByUsername(ctx, "c1", "alice")
	if err != nil {
		t.Fatalf("FindRosterEntryByUsername: %v", err)
	}
	if re.Status != store.RosterActive {
		t.Errorf("roster status = %q, want active", re.Status)
	}
}

// TestGiteaClassroomEndToEnd asserts that, once the Gitea-family adapter is wired,
// (i) a classroom with host: gitea passes host validation while an unknown host
// (bitbucket) is rejected with gitea named in the valid-hosts list, and (ii) a
// student claim against that classroom produces a gitea-stamped roster entry and
// enqueues provisioning — i.e. the "gitea" label routes end to end.
func TestGiteaClassroomEndToEnd(t *testing.T) {
	st := memory.New()
	q := &spyQueue{}
	r := fakeResolver{username: "alice", host: adapter.HostGitea}
	srv := New(Options{
		Store:            st,
		Queue:            q,
		Resolvers:        map[adapter.Host]identity.Resolver{adapter.HostGitea: r},
		Adapters:         map[adapter.Host]adapter.Adapter{adapter.HostGitea: &fakeWorkerAdapter{}},
		LoginHost:        adapter.HostGitea,
		GraderConfigured: true,
	})
	ctx := context.Background()

	// (i) Validation: host gitea accepted; bitbucket 400s and names gitea.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/classrooms",
		strings.NewReader(`{"name":"CS","host":"gitea","host_namespace":"cs-gitea"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create gitea classroom = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var cls store.Classroom
	if err := json.Unmarshal(rec.Body.Bytes(), &cls); err != nil {
		t.Fatal(err)
	}
	if cls.Host != adapter.HostGitea {
		t.Fatalf("classroom host = %q, want gitea", cls.Host)
	}

	bad := httptest.NewRecorder()
	srv.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, "/classrooms",
		strings.NewReader(`{"name":"X","host":"bitbucket","host_namespace":"org"}`)))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bitbucket host = %d, want 400; body=%s", bad.Code, bad.Body.String())
	}
	if !strings.Contains(bad.Body.String(), "gitea") {
		t.Errorf("valid-hosts message should list gitea: %s", bad.Body.String())
	}

	// (ii) Claim flow: a gitea assignment yields a gitea-stamped roster entry and
	// an enqueued provisioning job.
	if err := st.CreateAssignment(ctx, &store.Assignment{
		ID: "a1", ClassroomID: cls.ID, Slug: "hw1",
		TemplateRef: adapter.TemplateRef{Host: adapter.HostGitea, Namespace: "cs-gitea", Name: "hw1-template"},
	}); err != nil {
		t.Fatal(err)
	}
	claim := doAcceptCallback(srv, "a1")
	if claim.Code != http.StatusFound {
		t.Fatalf("claim = %d, want 302 (redirect to /me); body=%s", claim.Code, claim.Body.String())
	}
	re, err := st.FindRosterEntryByUsername(ctx, cls.ID, "alice")
	if err != nil {
		t.Fatalf("roster entry not created: %v", err)
	}
	if re.Host != adapter.HostGitea {
		t.Errorf("roster entry host = %q, want gitea", re.Host)
	}
	if len(q.jobs) == 0 {
		t.Error("want a provisioning job enqueued for the gitea submission")
	}
}
