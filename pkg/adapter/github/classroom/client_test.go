// SPDX-License-Identifier: Apache-2.0

package classroom

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseGrade(t *testing.T) {
	cases := []struct {
		in           string
		wantA, wantB float64
		ok           bool
	}{
		{"10/90", 10, 90, true},
		{"0/0", 0, 0, true},
		{" 8 / 10 ", 8, 10, true},
		{"87.5/100", 87.5, 100, true},
		{"", 0, 0, false},
		{"10", 0, 0, false},  // no denominator: the max is unknown, not zero
		{"n/a", 0, 0, false}, // seen in the wild
		{"10/", 0, 0, false},
		{"/90", 0, 0, false},
	}
	for _, c := range cases {
		a, b, ok := ParseGrade(c.in)
		if ok != c.ok || (ok && (a != c.wantA || b != c.wantB)) {
			t.Errorf("ParseGrade(%q) = %v, %v, %v; want %v, %v, %v", c.in, a, b, ok, c.wantA, c.wantB, c.ok)
		}
	}
}

func TestSplitFullName(t *testing.T) {
	cases := map[string][2]string{
		"CS-101-F26/hw-01-student01": {"CS-101-F26", "hw-01-student01"},
		"hw-01-student01":            {"", ""},
		"":                           {"", ""},
		"/name":                      {"", ""},
		"owner/":                     {"", ""},
	}
	for in, want := range cases {
		owner, name := SplitFullName(in)
		if owner != want[0] || name != want[1] {
			t.Errorf("SplitFullName(%q) = %q, %q; want %q, %q", in, owner, name, want[0], want[1])
		}
	}
}

func TestNewRequiresToken(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Error("expected an error without a token")
	}
}

// fakeAPI serves the handful of Classroom endpoints the importer uses, with
// payloads shaped exactly like the real ones.
func fakeAPI(t *testing.T) (*Client, *[]string) {
	t.Helper()
	var seen []string

	mux := http.NewServeMux()
	record := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			seen = append(seen, r.URL.Path)
			if got := r.Header.Get("Authorization"); got != "token t0ken" {
				t.Errorf("Authorization = %q", got)
			}
			h(w, r)
		}
	}
	writeJSONResp := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}

	mux.HandleFunc("/classrooms/77", record(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(w, map[string]any{
			"id": 77, "name": "CS-101", "archived": false,
			"organization": map[string]any{"id": 9, "login": "CS-101-F26"},
		})
	}))
	mux.HandleFunc("/classrooms/77/assignments", record(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page = %q, want 100", r.URL.Query().Get("per_page"))
		}
		if r.URL.Query().Get("page") != "1" {
			writeJSONResp(w, []any{})
			return
		}
		writeJSONResp(w, []map[string]any{{"id": 501, "title": "HW 1", "slug": "hw-01", "type": "individual"}})
	}))
	mux.HandleFunc("/assignments/501", record(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(w, map[string]any{
			"id": 501, "title": "HW 1", "slug": "hw-01", "type": "individual",
			"deadline": "2026-02-07T01:00:00Z",
			"accepted": 1,
			"starter_code_repository": map[string]any{
				"id": 1, "name": "starter", "full_name": "CS-101-F26/starter", "default_branch": "main",
			},
		})
	}))
	mux.HandleFunc("/assignments/501/accepted_assignments", record(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			writeJSONResp(w, []any{})
			return
		}
		writeJSONResp(w, []map[string]any{{
			"id": 900, "submitted": true, "commit_count": 3, "grade": "10/90",
			"students":   []map[string]any{{"id": 1, "login": "student01"}},
			"repository": map[string]any{"id": 2, "name": "hw-01-student01", "full_name": "CS-101-F26/hw-01-student01"},
		}})
	}))
	mux.HandleFunc("/user", record(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(w, map[string]any{"id": 42, "login": "instructor"})
	}))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := New(Config{Token: "t0ken", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, &seen
}

func TestFetchBuildsSnapshot(t *testing.T) {
	c, seen := fakeAPI(t)

	snap, err := Fetch(context.Background(), c, 77)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if snap.OrgLogin() != "CS-101-F26" {
		t.Errorf("org = %q", snap.OrgLogin())
	}
	if snap.Manifest.Source != "github-classroom" || snap.Manifest.Format != SnapshotFormat {
		t.Errorf("manifest = %+v", snap.Manifest)
	}
	if len(snap.Assignments) != 1 {
		t.Fatalf("got %d assignments, want 1", len(snap.Assignments))
	}
	rec := snap.Assignments[0]
	if rec.Assignment.StarterCodeRepository == nil {
		t.Error("Fetch must use the assignment detail response, which carries starter code")
	}
	want := time.Date(2026, 2, 7, 1, 0, 0, 0, time.UTC)
	if rec.Assignment.Deadline == nil || !rec.Assignment.Deadline.Equal(want) {
		t.Errorf("deadline = %v", rec.Assignment.Deadline)
	}
	if len(rec.Accepted) != 1 || rec.Accepted[0].Repository.Name != "hw-01-student01" {
		t.Errorf("accepted = %+v", rec.Accepted)
	}
	if rec.Accepted[0].Grade != "10/90" {
		t.Errorf("grade = %q", rec.Accepted[0].Grade)
	}

	// The endpoint carrying student legal names must never be requested.
	for _, path := range *seen {
		if strings.HasSuffix(path, "/grades") {
			t.Fatalf("the importer requested %s; that endpoint carries roster_identifier (a legal name)", path)
		}
	}
}

func TestAuthenticatedUser(t *testing.T) {
	c, _ := fakeAPI(t)
	me, err := c.AuthenticatedUser(context.Background())
	if err != nil {
		t.Fatalf("AuthenticatedUser: %v", err)
	}
	if me.Login != "instructor" {
		t.Errorf("login = %q", me.Login)
	}
}

// A null grade or deadline must decode to a zero value, not an error: GitHub sends
// null for both constantly.
func TestNullsDecodeCleanly(t *testing.T) {
	var acc AcceptedAssignment
	if err := json.Unmarshal([]byte(`{"grade":null,"students":[]}`), &acc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if acc.Grade != "" {
		t.Errorf("grade = %q, want empty", acc.Grade)
	}
	var as Assignment
	if err := json.Unmarshal([]byte(`{"deadline":null,"max_members":null}`), &as); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if as.Deadline != nil {
		t.Errorf("deadline = %v, want nil", as.Deadline)
	}
}

func TestListPagedFollowsPages(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		n := 0
		switch page {
		case "1":
			n = perPage // a full page means there may be more
		case "2":
			n = 7
		}
		out := make([]Classroom, n)
		for i := range out {
			out[i] = Classroom{ID: int64(i), Name: fmt.Sprintf("c%s-%d", page, i)}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	c, err := New(Config{Token: "t", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	all, err := c.ListClassrooms(context.Background())
	if err != nil {
		t.Fatalf("ListClassrooms: %v", err)
	}
	if len(all) != perPage+7 {
		t.Errorf("got %d classrooms, want %d", len(all), perPage+7)
	}
	if len(pages) != 2 {
		t.Errorf("requested pages %v, want exactly 2", pages)
	}
}

// An auth failure is the most likely first-run error, so it must say what to do.
func TestAPIErrorsExplainThemselves(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Requires authentication"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "bad", BaseURL: srv.URL})
	_, err := c.GetClassroom(context.Background(), 1)
	if err == nil {
		t.Fatal("expected an error")
	}
	if StatusOf(err) != http.StatusUnauthorized {
		t.Errorf("StatusOf = %d", StatusOf(err))
	}
	msg := err.Error()
	for _, want := range []string{"user", "read:org", "App installation token"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
}
