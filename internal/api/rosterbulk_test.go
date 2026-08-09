// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/internal/store/memory"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

type bulkResp struct {
	Created        int `json:"created"`
	AlreadyPresent int `json:"already_present"`
	Errors         int `json:"errors"`
	Results        []struct {
		Username string `json:"username"`
		Status   string `json:"status"`
		Error    string `json:"error"`
	} `json:"results"`
}

func seedBulkClassroom(t *testing.T, st *memory.Store) {
	t.Helper()
	if err := st.CreateClassroom(context.Background(), &store.Classroom{
		ID: "c1", Name: "CS101", Host: adapter.HostGitHub, HostNamespace: "cs101-org",
	}); err != nil {
		t.Fatal(err)
	}
}

func postBulk(t *testing.T, srv *Server, body string) (*httptest.ResponseRecorder, bulkResp) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/classrooms/c1/roster/bulk", strings.NewReader(body)))
	var out bulkResp
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode bulk response: %v; body=%s", err, rec.Body.String())
		}
	}
	return rec, out
}

// The core requirement: one bad row must not block the rest. A mix of valid,
// invalid, and already-present rows returns the right per-row status, and the
// valid rows are created even though a sibling row failed.
func TestBulkRosterPartialSuccess(t *testing.T) {
	srv, st, _ := newTestServer("alice")
	seedBulkClassroom(t, st)

	// bob is already on the roster before the request.
	if err := st.CreateRosterEntry(context.Background(), &store.RosterEntry{
		ID: "r-bob", ClassroomID: "c1", Host: adapter.HostGitHub,
		HostUsername: "bob", Status: store.RosterInvited,
	}); err != nil {
		t.Fatal(err)
	}

	rec, out := postBulk(t, srv, `{"entries":[
		{"username":"alice"},
		{"username":"has a space"},
		{"username":"bob"},
		{"username":""},
		{"username":"carol","email_hash":"abc123"}
	]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if out.Created != 2 || out.AlreadyPresent != 1 || out.Errors != 2 {
		t.Errorf("counts = created %d / already %d / errors %d; want 2/1/2",
			out.Created, out.AlreadyPresent, out.Errors)
	}

	want := []string{"created", "error", "already_present", "error", "created"}
	if len(out.Results) != len(want) {
		t.Fatalf("results = %d rows, want %d", len(out.Results), len(want))
	}
	for i, w := range want {
		if out.Results[i].Status != w {
			t.Errorf("row %d (%q) status = %q, want %q", i, out.Results[i].Username, out.Results[i].Status, w)
		}
	}
	// An error row must say why, so the instructor can fix that line.
	if out.Results[1].Error == "" {
		t.Error("invalid username row returned no error message")
	}

	// The valid rows really landed, despite the failures beside them.
	for _, u := range []string{"alice", "carol"} {
		if _, err := st.FindRosterEntryByUsername(context.Background(), "c1", u); err != nil {
			t.Errorf("%s was not created: %v", u, err)
		}
	}
	// The invalid ones did not.
	if _, err := st.FindRosterEntryByUsername(context.Background(), "c1", "has a space"); err == nil {
		t.Error("invalid username was created")
	}
	// email_hash is stored as given.
	if re, err := st.FindRosterEntryByUsername(context.Background(), "c1", "carol"); err != nil {
		t.Fatal(err)
	} else if re.EmailHash != "abc123" {
		t.Errorf("carol email_hash = %q, want %q", re.EmailHash, "abc123")
	}
}

// Re-submitting the same list creates nothing the second time — the instructor
// fixes one typo, pastes the whole roster again, and gets no duplicates.
func TestBulkRosterIdempotentOnResubmit(t *testing.T) {
	srv, st, _ := newTestServer("alice")
	seedBulkClassroom(t, st)

	body := `{"entries":[{"username":"alice"},{"username":"bob"},{"username":"carol"}]}`

	_, first := postBulk(t, srv, body)
	if first.Created != 3 {
		t.Fatalf("first submit created %d, want 3", first.Created)
	}

	_, second := postBulk(t, srv, body)
	if second.Created != 0 || second.AlreadyPresent != 3 {
		t.Errorf("second submit = created %d / already %d; want 0/3", second.Created, second.AlreadyPresent)
	}

	roster, err := st.ListRosterEntries(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(roster) != 3 {
		t.Errorf("roster has %d entries after re-submit, want 3 (no duplicates)", len(roster))
	}
}

// The same username twice within one request must not create two entries.
func TestBulkRosterDedupesWithinOneRequest(t *testing.T) {
	srv, st, _ := newTestServer("alice")
	seedBulkClassroom(t, st)

	_, out := postBulk(t, srv, `{"entries":[{"username":"alice"},{"username":"alice"}]}`)
	if out.Created != 1 || out.AlreadyPresent != 1 {
		t.Errorf("counts = created %d / already %d; want 1/1", out.Created, out.AlreadyPresent)
	}

	roster, err := st.ListRosterEntries(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(roster) != 1 {
		t.Errorf("roster has %d entries, want 1", len(roster))
	}
}

// A wrong-file paste fails fast and whole, rather than importing garbage.
func TestBulkRosterRejectsOversizedRequest(t *testing.T) {
	srv, st, _ := newTestServer("alice")
	seedBulkClassroom(t, st)

	rows := make([]string, 0, maxBulkRosterRows+1)
	for i := 0; i <= maxBulkRosterRows; i++ {
		rows = append(rows, fmt.Sprintf(`{"username":"student%d"}`, i))
	}
	rec, _ := postBulk(t, srv, `{"entries":[`+strings.Join(rows, ",")+`]}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	roster, err := st.ListRosterEntries(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(roster) != 0 {
		t.Errorf("oversized request created %d entries; want 0 (nothing partially imported)", len(roster))
	}
}

func TestBulkRosterRejectsEmptyList(t *testing.T) {
	srv, st, _ := newTestServer("alice")
	seedBulkClassroom(t, st)

	rec, _ := postBulk(t, srv, `{"entries":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// The single-add endpoint's contract is unchanged by the bulk addition.
func TestSingleAddRosterStillWorks(t *testing.T) {
	srv, st, _ := newTestServer("alice")
	seedBulkClassroom(t, st)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/classrooms/c1/roster",
		strings.NewReader(`{"username":"solo"}`)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("single add = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var re store.RosterEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &re); err != nil {
		t.Fatalf("single add response is not a RosterEntry: %v", err)
	}
	if re.HostUsername != "solo" {
		t.Errorf("username = %q, want %q", re.HostUsername, "solo")
	}
	if _, err := st.FindRosterEntryByUsername(context.Background(), "c1", "solo"); err != nil {
		t.Errorf("solo not created: %v", err)
	}
}
