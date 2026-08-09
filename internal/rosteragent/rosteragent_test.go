// SPDX-License-Identifier: AGPL-3.0-or-later

package rosteragent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- connectors -------------------------------------------------------------

func TestBrightspaceExportParsesClasslist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classlist.csv")
	// Shape of a real Brightspace classlist export: extra columns, quoted
	// "Last, First" names, a blank trailing line.
	csv := "Username,OrgDefinedId,Last Name,First Name,Email,Role\n" +
		"jdoe,00123,Doe,Jane,jane.doe@example.edu,Student\n" +
		"bsmith,00124,Smith,Bob,bob.smith@example.edu,Student\n" +
		"\n"
	if err := os.WriteFile(path, []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}

	rows, err := (&BrightspaceExport{Path: path}).FetchRoster(context.Background())
	if err != nil {
		t.Fatalf("FetchRoster: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].Name != "Jane Doe" {
		t.Errorf("name = %q, want %q", rows[0].Name, "Jane Doe")
	}
	if rows[0].Email != "jane.doe@example.edu" {
		t.Errorf("email = %q", rows[0].Email)
	}
	if rows[0].LMSUserID != "jdoe" {
		t.Errorf("lms id = %q, want %q", rows[0].LMSUserID, "jdoe")
	}
}

// A "Name" column is used directly when present, instead of First/Last.
func TestBrightspaceExportSingleNameColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.csv")
	if err := os.WriteFile(path, []byte("Name,Email\nJane Doe,jane@example.edu\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, err := (&BrightspaceExport{Path: path}).FetchRoster(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "Jane Doe" {
		t.Fatalf("rows = %+v", rows)
	}
}

// A file with no recognizable name column must fail loudly and point at the
// manual path, not silently return an empty roster.
func TestBrightspaceExportUnrecognizedFileFailsLoudly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrong.csv")
	if err := os.WriteFile(path, []byte("col_a,col_b\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (&BrightspaceExport{Path: path}).FetchRoster(context.Background())
	if err == nil {
		t.Fatal("expected an error for a file with no name column")
	}
	if !strings.Contains(err.Error(), "Bulk add") {
		t.Errorf("error does not point at manual bulk entry: %v", err)
	}
}

func TestBrightspaceExportMissingFilePointsToManualPath(t *testing.T) {
	_, err := (&BrightspaceExport{Path: "/nonexistent/classlist.csv"}).FetchRoster(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Bulk add") {
		t.Errorf("error does not point at manual bulk entry: %v", err)
	}
}

func TestBrightspaceAPIParsesClasslist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok123" {
			t.Errorf("Authorization = %q", got)
		}
		if !strings.Contains(r.URL.Path, "/classlist/") {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[
			{"Identifier":"111","DisplayName":"Jane Doe","Email":"jane@example.edu","OrgDefinedId":"00123"},
			{"Identifier":"112","FirstName":"Bob","LastName":"Smith","Email":"bob@example.edu"}
		]`))
	}))
	defer srv.Close()

	rows, err := (&BrightspaceAPI{
		BaseURL: srv.URL, AccessToken: "tok123", OrgUnitID: "999", HTTP: srv.Client(),
	}).FetchRoster(context.Background())
	if err != nil {
		t.Fatalf("FetchRoster: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].Name != "Jane Doe" || rows[0].LMSUserID != "00123" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	// DisplayName absent → composed from First/Last.
	if rows[1].Name != "Bob Smith" {
		t.Errorf("row 1 name = %q, want %q", rows[1].Name, "Bob Smith")
	}
}

// The expected real-world outcome: an instructor's token is refused, and the
// message must explain the administrator gate rather than just "403".
func TestBrightspaceAPIForbiddenExplainsAdminGate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := (&BrightspaceAPI{
		BaseURL: srv.URL, AccessToken: "bad", OrgUnitID: "1", HTTP: srv.Client(),
	}).FetchRoster(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"administrator", "--export", "Bulk add"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

// --- matching ---------------------------------------------------------------

func TestMatchTiers(t *testing.T) {
	candidates := []Candidate{
		{Username: "jdoe", FullName: "Jane Doe"},
		{Username: "bsmith"},
		{Username: "carol-m", FullName: "Carol Mendez"},
	}

	tests := []struct {
		name     string
		row      RosterRow
		want     MatchStatus
		wantUser string
	}{
		{
			name:     "exact name match is accepted automatically",
			row:      RosterRow{Name: "Jane Doe", Email: "x@example.edu"},
			want:     MatchExact,
			wantUser: "jdoe",
		},
		{
			name:     "email local-part equal to a username is exact",
			row:      RosterRow{Name: "Robert Smith", Email: "bsmith@example.edu"},
			want:     MatchExact,
			wantUser: "bsmith",
		},
		{
			name:     "reordered name needs confirmation, not a silent guess",
			row:      RosterRow{Name: "Mendez, Carol"},
			want:     MatchNeedsConfirm,
			wantUser: "carol-m",
		},
		{
			name: "no candidate is flagged, never invented",
			row:  RosterRow{Name: "Someone Else", Email: "nobody@example.edu"},
			want: MatchNone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchRoster([]RosterRow{tc.row}, candidates)[0]
			if got.Status != tc.want {
				t.Errorf("status = %q, want %q (why: %s)", got.Status, tc.want, got.Why)
			}
			if got.Username != tc.wantUser {
				t.Errorf("username = %q, want %q", got.Username, tc.wantUser)
			}
			if got.Why == "" {
				t.Error("every match must explain itself for the audit log")
			}
		})
	}
}

// Two students with the same name must not be auto-assigned to whichever
// candidate sorted first — and the tie has to be *resolvable*, not just
// detected, so the tied candidates come back with the match.
func TestMatchSameNameIsAmbiguousAndCarriesCandidates(t *testing.T) {
	candidates := []Candidate{
		{Username: "jdoe2", FullName: "Jane Doe"},
		{Username: "jdoe1", FullName: "Jane Doe"},
	}
	got := MatchRoster([]RosterRow{{Name: "Jane Doe"}}, candidates)[0]

	if got.Status != MatchAmbiguous {
		t.Fatalf("status = %q, want %q so the instructor can resolve it", got.Status, MatchAmbiguous)
	}
	if got.Username != "" {
		t.Errorf("username = %q, want empty until the instructor chooses", got.Username)
	}
	if len(got.Candidates) != 2 {
		t.Fatalf("candidates = %d, want both tied options carried for selection", len(got.Candidates))
	}
	// Sorted so the numbered list an instructor sees is stable between runs.
	if got.Candidates[0].Username != "jdoe1" || got.Candidates[1].Username != "jdoe2" {
		t.Errorf("candidates not sorted by username: %+v", got.Candidates)
	}
	if !strings.Contains(got.Why, "jdoe1") || !strings.Contains(got.Why, "jdoe2") {
		t.Errorf("why should name the options for the audit log, got %q", got.Why)
	}
}

// A reordered name matching several candidates is the other ambiguity path.
func TestMatchSeveralNearMatchesIsAmbiguous(t *testing.T) {
	candidates := []Candidate{
		{Username: "jdoe1", FullName: "Jane Doe"},
		{Username: "jdoe2", FullName: "Jane Doe"},
	}
	got := MatchRoster([]RosterRow{{Name: "Doe, Jane"}}, candidates)[0]
	if got.Status != MatchAmbiguous {
		t.Errorf("status = %q, want %q", got.Status, MatchAmbiguous)
	}
	if len(got.Candidates) != 2 {
		t.Errorf("candidates = %d, want 2", len(got.Candidates))
	}
}

// Resolve turns a tie into a confirmed match, and refuses anything that was not
// on the list — a mistyped answer must not enrol an unoffered student.
func TestResolveAppliesOnlyAListedCandidate(t *testing.T) {
	candidates := []Candidate{
		{Username: "jdoe1", FullName: "Jane Doe"},
		{Username: "jdoe2", FullName: "Jane Doe"},
	}
	m := MatchRoster([]RosterRow{{Name: "Jane Doe", Email: "jane@example.edu"}}, candidates)[0]

	if err := m.Resolve("someone-else"); err == nil {
		t.Error("Resolve accepted a username that was not among the candidates")
	}
	if m.Username != "" {
		t.Errorf("a rejected Resolve must not mutate the match; username = %q", m.Username)
	}

	if err := m.Resolve("jdoe2"); err != nil {
		t.Fatalf("Resolve(jdoe2): %v", err)
	}
	if m.Username != "jdoe2" {
		t.Errorf("username = %q, want jdoe2", m.Username)
	}
	if m.Status != MatchNeedsConfirm {
		t.Errorf("status = %q, want %q after resolution", m.Status, MatchNeedsConfirm)
	}
	if len(m.Candidates) != 0 {
		t.Error("resolved match should no longer carry the tied candidates")
	}
}

// The chosen candidate must round-trip into the payload — the whole point of
// resolving rather than skipping.
func TestResolvedAmbiguityRoundTripsIntoPayload(t *testing.T) {
	candidates := []Candidate{
		{Username: "jdoe1", FullName: "Jane Doe"},
		{Username: "jdoe2", FullName: "Jane Doe"},
	}
	for _, pick := range []string{"jdoe1", "jdoe2"} {
		m := MatchRoster([]RosterRow{{Name: "Jane Doe", Email: "jane@example.edu"}}, candidates)[0]
		if err := m.Resolve(pick); err != nil {
			t.Fatal(err)
		}
		req, skipped := BuildPayload("c1", []Match{m})
		if len(skipped) != 0 {
			t.Errorf("pick %s: resolved match was skipped", pick)
		}
		if len(req.Entries) != 1 || req.Entries[0].Username != pick {
			t.Fatalf("pick %s: entries = %+v", pick, req.Entries)
		}
		if req.Entries[0].EmailHash != HashEmail("c1", "jane@example.edu") {
			t.Errorf("pick %s: email hash not carried through resolution", pick)
		}
	}
}

// An unresolved tie must still be reported as skipped, never silently dropped —
// BuildPayload's existing skipped-row contract has to cover the new status too.
func TestUnresolvedAmbiguityIsReportedAsSkipped(t *testing.T) {
	candidates := []Candidate{
		{Username: "jdoe1", FullName: "Jane Doe"},
		{Username: "jdoe2", FullName: "Jane Doe"},
	}
	m := MatchRoster([]RosterRow{{Name: "Jane Doe"}}, candidates)[0]

	req, skipped := BuildPayload("c1", []Match{m})
	if len(req.Entries) != 0 {
		t.Errorf("an unresolved tie was sent: %+v", req.Entries)
	}
	if len(skipped) != 1 {
		t.Fatalf("skipped = %d, want the unresolved row reported back", len(skipped))
	}
	if skipped[0].Status != MatchAmbiguous || skipped[0].Why == "" {
		t.Errorf("skipped row lost its reason: %+v", skipped[0])
	}
}

// A single exact name match is unaffected by the tie handling.
func TestSingleExactNameStillMatchesDirectly(t *testing.T) {
	got := MatchRoster(
		[]RosterRow{{Name: "Jane Doe"}},
		[]Candidate{{Username: "jdoe", FullName: "Jane Doe"}, {Username: "other", FullName: "Someone Else"}},
	)[0]
	if got.Status != MatchExact || got.Username != "jdoe" {
		t.Errorf("got %+v, want an exact match on jdoe", got)
	}
}

// The email local-part is unique per student, so it still wins outright even
// when several candidates share the LMS display name.
func TestEmailLocalPartBreaksASameNameTie(t *testing.T) {
	candidates := []Candidate{
		{Username: "jdoe1", FullName: "Jane Doe"},
		{Username: "jdoe2", FullName: "Jane Doe"},
	}
	got := MatchRoster([]RosterRow{{Name: "Jane Doe", Email: "jdoe2@example.edu"}}, candidates)[0]
	if got.Status != MatchExact || got.Username != "jdoe2" {
		t.Errorf("got %+v, want the email to break the tie to jdoe2", got)
	}
}

// --- hashing and payload ----------------------------------------------------

// The digest must equal the dashboard's client-side hash for the same input, or
// a roster entered half by hand and half by the agent produces two different
// hashes for one student.
//
// These vectors were verified against web/src/roster-parse.ts's toBulkEntries()
// by running both implementations over the same inputs (2026-08-09). If this
// test fails, the two hashing paths have diverged and the agent's output is no
// longer compatible with the dashboard's — fix, do not re-baseline.
func TestHashEmailMatchesDashboardFormula(t *testing.T) {
	vectors := map[[2]string]string{
		{"c1", "jane@example.edu"}:           "73e06298d3d651919c30c0d078e0bb8062b8250cf47553bdcbe822f90e44edfc",
		{"c2", "jane@example.edu"}:           "db8501dd277c4f2b0bde0068786a83a96cd15b37a3c1e49e20bbd68d407a5276",
		{"abc-123", "bob.smith@arizona.edu"}: "923469e646c0b6b3f66f372f23d94e595c07efe1f6172d0e80b645d824fd9370",
	}
	for in, want := range vectors {
		if got := HashEmail(in[0], in[1]); got != want {
			t.Errorf("HashEmail(%q, %q) = %q, want %q (diverged from roster-parse.ts)", in[0], in[1], got, want)
		}
	}

	got := HashEmail("c1", "jane@example.edu")
	if len(got) != 64 {
		t.Fatalf("digest length = %d, want 64 hex chars", len(got))
	}
	// Case and surrounding space must not change the digest.
	if up := HashEmail("c1", "  JANE@EXAMPLE.EDU "); up != got {
		t.Errorf("hashing is not case/space insensitive: %q vs %q", up, got)
	}
	// Salting: the same address in another classroom must differ.
	if other := HashEmail("c2", "jane@example.edu"); other == got {
		t.Error("digest is not salted per classroom")
	}
}

func TestBuildPayloadSendsOnlyUsernameAndHash(t *testing.T) {
	matches := []Match{
		{Row: RosterRow{Name: "Jane Doe", Email: "jane@example.edu", LMSUserID: "00123"}, Username: "jdoe", Status: MatchExact},
		{Row: RosterRow{Name: "No Match", Email: "nm@example.edu"}, Status: MatchNone},
		{Row: RosterRow{Name: "No Email"}, Username: "noemail", Status: MatchExact},
	}

	req, skipped := BuildPayload("c1", matches)

	if len(req.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(req.Entries))
	}
	if len(skipped) != 1 || skipped[0].Row.Name != "No Match" {
		t.Errorf("skipped = %+v, want the unmatched row reported back", skipped)
	}
	// A student with no email sends no hash at all, rather than a hash of "".
	if req.Entries[1].EmailHash != "" {
		t.Errorf("entry without an email got a hash: %q", req.Entries[1].EmailHash)
	}

	// The privacy assertion: no name, no plaintext email, no LMS ID in the wire form.
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"Jane Doe", "jane@example.edu", "00123", "No Match", "nm@example.edu"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("payload leaks %q: %s", leak, body)
		}
	}
}

// The payload must deserialize into exactly the shape CC-CA6's endpoint parses.
func TestPayloadMatchesBulkEndpointShape(t *testing.T) {
	req, _ := BuildPayload("c1", []Match{
		{Row: RosterRow{Name: "Jane Doe", Email: "jane@example.edu"}, Username: "jdoe", Status: MatchExact},
	})
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	// This mirrors handleAddRosterBulk's anonymous input struct field-for-field.
	var server struct {
		Entries []struct {
			Username  string `json:"username"`
			EmailHash string `json:"email_hash"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(body, &server); err != nil {
		t.Fatalf("server cannot parse the agent's payload: %v", err)
	}
	if len(server.Entries) != 1 {
		t.Fatalf("server parsed %d entries, want 1", len(server.Entries))
	}
	if server.Entries[0].Username != "jdoe" {
		t.Errorf("username = %q", server.Entries[0].Username)
	}
	if len(server.Entries[0].EmailHash) != 64 {
		t.Errorf("email_hash = %q, want a 64-char digest", server.Entries[0].EmailHash)
	}
}

// MatchRoster and HashEmail must not perform any network I/O — the entire
// privacy argument depends on it. Any dial attempt fails this test.
func TestMatchingAndHashingDoNoNetworkIO(t *testing.T) {
	orig := http.DefaultTransport
	http.DefaultTransport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("unexpected network call during local matching/hashing: %s", r.URL)
		return nil, http.ErrUseLastResponse
	})
	defer func() { http.DefaultTransport = orig }()

	rows := []RosterRow{{Name: "Jane Doe", Email: "jane@example.edu"}}
	matches := MatchRoster(rows, []Candidate{{Username: "jdoe", FullName: "Jane Doe"}})
	if _, _ = BuildPayload("c1", matches); false {
		t.Fatal("unreachable")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// --- honest failure ---------------------------------------------------------

func TestErrNoConnectorPointsAtManualEntry(t *testing.T) {
	err := ErrNoConnector{LMS: "moodle"}
	msg := err.Error()
	for _, want := range []string{"moodle", "Bulk add", "roster/bulk"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q: %s", want, msg)
		}
	}
}
