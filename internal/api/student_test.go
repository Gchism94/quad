// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/grading"
	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/internal/store/memory"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

// studentCookie mints a student session directly (bypassing OAuth) and returns
// the matching cookie, so API scoping can be tested without a live resolver.
func studentCookie(srv *Server, host adapter.Host, username string) *http.Cookie {
	token := "sess-" + username
	srv.sessMu.Lock()
	srv.sessions[token] = session{username: username, host: host, created: time.Now(), isOperator: false}
	srv.sessMu.Unlock()
	return &http.Cookie{Name: sessionCookie, Value: token}
}

func seedStudentData(t *testing.T, st *memory.Store) {
	t.Helper()
	ctx := context.Background()
	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c1", Name: "CS101", Host: adapter.HostGitHub, HostNamespace: "org1"})
	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c2", Name: "CS201", Host: adapter.HostGitHub, HostNamespace: "org2"})
	dl := time.Now().Add(48 * time.Hour)
	_ = st.CreateAssignment(ctx, &store.Assignment{ID: "a1", ClassroomID: "c1", Title: "Homework 1", Slug: "hw-1", Deadline: &dl})
	_ = st.CreateAssignment(ctx, &store.Assignment{ID: "a2", ClassroomID: "c2", Title: "Homework 2", Slug: "hw-2"})
	_ = st.CreateRosterEntry(ctx, &store.RosterEntry{ID: "ra1", ClassroomID: "c1", Host: adapter.HostGitHub, HostUsername: "alice", Status: store.RosterActive})
	_ = st.CreateRosterEntry(ctx, &store.RosterEntry{ID: "ra2", ClassroomID: "c2", Host: adapter.HostGitHub, HostUsername: "alice", Status: store.RosterActive})
	_ = st.CreateRosterEntry(ctx, &store.RosterEntry{ID: "rb1", ClassroomID: "c1", Host: adapter.HostGitHub, HostUsername: "bob", Status: store.RosterActive})
	older := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(-1 * time.Hour)
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1", RosterEntryID: "ra1", Status: "active", LastActivityAt: &older, Repo: adapter.RepoRef{Host: adapter.HostGitHub, Namespace: "org1", Name: "hw-1-alice"}})
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s2", AssignmentID: "a2", RosterEntryID: "ra2", Status: "active", LastActivityAt: &newer, Repo: adapter.RepoRef{Host: adapter.HostGitHub, Namespace: "org2", Name: "hw-2-alice"}})
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s3", AssignmentID: "a1", RosterEntryID: "rb1", Status: "active", Repo: adapter.RepoRef{Host: adapter.HostGitHub, Namespace: "org1", Name: "hw-1-bob"}})
}

func TestMyWorkListsOwnSubmissions(t *testing.T) {
	srv, st, _ := newTestServer("operator")
	seedStudentData(t, st)
	// A running grade on s1 so grading_status surfaces.
	now := time.Now()
	_ = st.CreateGradingRun(context.Background(), &store.GradingRun{ID: "run1", SubmissionID: "s1", Status: "running", StartedAt: &now})

	req := httptest.NewRequest(http.MethodGet, "/me/work", nil)
	req.AddCookie(studentCookie(srv, adapter.HostGitHub, "alice"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Work []workItem `json:"work"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Work) != 2 {
		t.Fatalf("alice work count = %d, want 2", len(out.Work))
	}
	// Newest activity first: s2 then s1.
	if out.Work[0].SubmissionID != "s2" || out.Work[1].SubmissionID != "s1" {
		t.Errorf("order = [%s,%s], want [s2,s1]", out.Work[0].SubmissionID, out.Work[1].SubmissionID)
	}
	// Enrichment: classroom name, repo link, deadline, grading status.
	var s1 *workItem
	for i := range out.Work {
		if out.Work[i].SubmissionID == "s1" {
			s1 = &out.Work[i]
		}
	}
	if s1.AssignmentTitle != "Homework 1" || s1.ClassroomName != "CS101" {
		t.Errorf("s1 enrichment = %+v", s1)
	}
	if s1.RepoWebURL != "https://github.test/org1/hw-1-alice" {
		t.Errorf("s1 repo url = %q", s1.RepoWebURL)
	}
	if s1.GradingStatus != "running" {
		t.Errorf("s1 grading status = %q, want running", s1.GradingStatus)
	}
}

func TestMyWorkIsolation(t *testing.T) {
	srv, st, _ := newTestServer("operator")
	seedStudentData(t, st)

	req := httptest.NewRequest(http.MethodGet, "/me/work", nil)
	req.AddCookie(studentCookie(srv, adapter.HostGitHub, "bob"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out struct {
		Work []workItem `json:"work"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Work) != 1 || out.Work[0].SubmissionID != "s3" {
		t.Fatalf("bob work = %+v, want exactly [s3]", out.Work)
	}
}

func TestMyWorkUnauthenticated401(t *testing.T) {
	srv, st, _ := newTestServer("operator")
	seedStudentData(t, st)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/me/work", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", rec.Code)
	}
}

func TestMyWorkDetailNotOwned404(t *testing.T) {
	srv, st, _ := newTestServer("operator")
	seedStudentData(t, st)

	// alice asks for s3, which belongs to bob → 404 (no existence leak).
	req := httptest.NewRequest(http.MethodGet, "/me/work/s3", nil)
	req.AddCookie(studentCookie(srv, adapter.HostGitHub, "alice"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("not-owned detail status = %d, want 404", rec.Code)
	}
}

func TestMyWorkDetailBreakdownAndHistory(t *testing.T) {
	srv, st, _ := newTestServer("operator")
	seedStudentData(t, st)
	ctx := context.Background()

	// Two attempts on s1; the latest carries a per-test breakdown.
	res := grading.Result{
		Score: 5, MaxScore: 10,
		Tests: []grading.TestResult{
			{Name: "compiles", Passed: true, Points: 5, MaxPoints: 5},
			{Name: "edge-cases", Passed: false, Points: 0, MaxPoints: 5, Detail: "panic on empty input"},
		},
	}
	breakdown, _ := json.Marshal(res)
	_ = st.CreateGrade(ctx, &store.Grade{ID: "g1", SubmissionID: "s1", Score: 3, MaxScore: 10, GradedAt: time.Now().Add(-time.Hour)})
	_ = st.CreateGrade(ctx, &store.Grade{ID: "g2", SubmissionID: "s1", Score: 5, MaxScore: 10, Breakdown: breakdown, GradedAt: time.Now()})

	req := httptest.NewRequest(http.MethodGet, "/me/work/s1", nil)
	req.AddCookie(studentCookie(srv, adapter.HostGitHub, "alice"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var d workDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Tests) != 2 {
		t.Fatalf("tests = %d, want 2", len(d.Tests))
	}
	if d.Tests[0].Name != "compiles" || !d.Tests[0].Passed {
		t.Errorf("test[0] = %+v", d.Tests[0])
	}
	if d.Tests[1].Detail != "panic on empty input" {
		t.Errorf("test[1] detail = %q", d.Tests[1].Detail)
	}
	if len(d.History) != 2 {
		t.Fatalf("history = %d, want 2", len(d.History))
	}
	// Most recent first.
	if d.History[0].Score != 5 || d.History[1].Score != 3 {
		t.Errorf("history scores = [%v,%v], want [5,3]", d.History[0].Score, d.History[1].Score)
	}
	if d.LatestGrade == nil || d.LatestGrade.Score != 5 {
		t.Errorf("latest grade = %+v, want score 5", d.LatestGrade)
	}
}

// TestStudentSessionCannotAccessOperatorRoutes is the privilege-escalation guard:
// a student session (isOperator=false) shares the sessions map with operators, so
// it must NOT satisfy admin-gated routes even when auth is enabled.
func TestStudentSessionCannotAccessOperatorRoutes(t *testing.T) {
	srv, st := newAuthServer("alice") // auth enabled; only alice is an operator
	seedStudentData(t, st)

	// A student session for "alice" — same username as the admin, but NOT an
	// operator session — must still be rejected on operator routes.
	req := httptest.NewRequest(http.MethodGet, "/classrooms", nil)
	req.AddCookie(studentCookie(srv, adapter.HostGitHub, "alice"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("student session on operator route = %d, want 401", rec.Code)
	}

	// But the same student session works on the student route.
	req2 := httptest.NewRequest(http.MethodGet, "/me/work", nil)
	req2.AddCookie(studentCookie(srv, adapter.HostGitHub, "alice"))
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("student session on /me/work = %d, want 200", rec2.Code)
	}
}

// TestStudentPageServed confirms /me serves HTML without requiring a session
// (the page itself is public; its data fetch enforces auth).
func TestStudentPageServed(t *testing.T) {
	srv, _, _ := newTestServer("operator")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/me", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/me status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content-type = %q", ct)
	}
	if body := rec.Body.String(); !strings.Contains(body, "/me/work") || !strings.Contains(body, "/student/login") {
		t.Error("page should reference /me/work and the sign-in link")
	}
}
