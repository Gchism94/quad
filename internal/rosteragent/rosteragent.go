// SPDX-License-Identifier: AGPL-3.0-or-later

// Package rosteragent pulls a class roster from an LMS, matches each student to
// a Git-host username on the instructor's own machine, and hands the result to
// Cairn's bulk roster endpoint.
//
// The design constraint that shapes everything here: **the student's name and
// email never leave the machine running this agent.** They are read from the
// LMS, matched locally, and the email is reduced to a salted one-way digest
// before anything is sent. Cairn's server receives only {username, email_hash},
// which is exactly what store.RosterEntry documents it may hold — "MUST NOT
// carry a legal name, SIS ID, or plaintext email."
//
// This is why matching is local rather than server-side: sending names to the
// server to be matched there would defeat the model, however convenient it
// would be.
package rosteragent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// RosterRow is one student as the LMS reports them, before any matching. Every
// field here is potentially identifying and none of it is ever transmitted.
type RosterRow struct {
	Name      string
	LMSUserID string
	Email     string
}

// Connector fetches a roster from one LMS. Implementations are either
// token-based (a real LMS API) or file/scrape-based (an export the instructor
// already has access to), depending on what that LMS actually permits — see
// docs/lms-roster-agent.md for why Brightspace is not token-based by default.
type Connector interface {
	// Name identifies the connector in audit output, e.g. "brightspace-api".
	Name() string
	// FetchRoster returns the roster, or an error explaining what an instructor
	// should do instead. Errors here are expected and must stay actionable:
	// the agent's fallback is CC-CA6's manual bulk entry, never a silent partial.
	FetchRoster(ctx context.Context) ([]RosterRow, error)
}

// MatchStatus is how confident the agent is in a name→username match.
type MatchStatus string

const (
	// MatchExact is an unambiguous match; safe to accept without asking.
	MatchExact MatchStatus = "exact"
	// MatchNeedsConfirm is a plausible single candidate that is not exact —
	// a name variant, a nickname, a reordered "Last, First". The instructor
	// confirms before it is used. The agent never silently guesses.
	MatchNeedsConfirm MatchStatus = "needs_confirm"
	// MatchAmbiguous means several candidates tie and the agent cannot choose
	// between them — two students with the same name, most often. The tied
	// candidates are carried in Match.Candidates so the caller can ask the
	// instructor to pick; without a choice the row stays unused, but it is
	// resolvable rather than a dead end.
	MatchAmbiguous MatchStatus = "ambiguous"
	// MatchNone means no candidate was found. The row is reported, not dropped.
	MatchNone MatchStatus = "unmatched"
)

// Match pairs one LMS row with a candidate Git username.
type Match struct {
	Row      RosterRow
	Username string
	Status   MatchStatus
	// Candidates holds the tied options when Status is MatchAmbiguous, in a
	// stable order, so a caller can present them for selection. It is empty for
	// every other status.
	Candidates []Candidate
	// Why explains the decision in audit output, e.g. "exact name match" or
	// "no candidate above threshold".
	Why string
}

// Resolve records the instructor's choice for an ambiguous match, turning it
// into a confirmed one. It refuses a username that was not among the tied
// candidates, so a typo at the prompt cannot enrol someone who was never
// offered.
func (m *Match) Resolve(username string) error {
	for _, c := range m.Candidates {
		if c.Username == username {
			m.Username = username
			m.Status = MatchNeedsConfirm
			m.Why = "ambiguous; resolved by the instructor"
			m.Candidates = nil
			return nil
		}
	}
	return fmt.Errorf("%q is not one of the candidates for %q", username, m.Row.Name)
}

// Candidate is a known Git-host username the matcher can draw from — typically
// usernames the instructor supplies, or ones already on the Cairn roster.
type Candidate struct {
	Username string
	// FullName is optional. When present it is what the LMS name is compared
	// against; without it only the username itself is compared.
	FullName string
}

// Match pairs every LMS row against the candidate usernames, locally.
//
// No network call happens in this function, and none may be added: the whole
// privacy argument rests on names being compared on the instructor's machine.
// The tests assert this by matching against an empty transport.
func MatchRoster(rows []RosterRow, candidates []Candidate) []Match {
	out := make([]Match, 0, len(rows))
	for _, row := range rows {
		out = append(out, matchOne(row, candidates))
	}
	return out
}

func matchOne(row RosterRow, candidates []Candidate) Match {
	target := normalizeName(row.Name)
	// An email local-part is often the username or close to it, so it is a
	// useful secondary signal — but only ever compared locally.
	localPart := emailLocalPart(row.Email)

	// Collect every exact-name hit rather than returning the first. Two students
	// sharing a name is common enough that taking whichever sorted first would
	// silently enrol the wrong one — the failure this whole tiering exists to
	// avoid.
	var exactByName []Candidate
	for _, c := range candidates {
		if c.FullName != "" && normalizeName(c.FullName) == target {
			exactByName = append(exactByName, c)
		}
		// The email local-part equalling a username is a stronger signal than a
		// shared display name, and is unique per student, so it wins outright.
		if localPart != "" && strings.EqualFold(localPart, c.Username) {
			return Match{Row: row, Username: c.Username, Status: MatchExact, Why: "email local-part equals username"}
		}
	}
	switch len(exactByName) {
	case 1:
		return Match{Row: row, Username: exactByName[0].Username, Status: MatchExact, Why: "exact name match"}
	case 0:
		// fall through to the weaker signals below
	default:
		// Identical full names still get the initial tiebreak: two candidates
		// can both equal the LMS name yet differ by a middle initial the
		// normalisation kept, and narrowing beats making the instructor choose.
		return narrowByInitials(row, exactByName, "same name")
	}

	var confirm []Candidate

	// Weaker signals need a human. Collect rather than pick.
	for _, c := range candidates {
		switch {
		case c.FullName != "" && sameNameParts(c.FullName, row.Name):
			confirm = append(confirm, c)
		case localPart != "" && strings.EqualFold(squash(localPart), squash(c.Username)):
			confirm = append(confirm, c)
		case target != "" && strings.EqualFold(squash(row.Name), squash(c.Username)):
			confirm = append(confirm, c)
		}
	}

	switch len(confirm) {
	case 0:
		return Match{Row: row, Status: MatchNone, Why: "no candidate matched"}
	case 1:
		return Match{Row: row, Username: confirm[0].Username, Status: MatchNeedsConfirm, Why: "single near match — confirm before use"}
	default:
		return narrowByInitials(row, confirm, "several near matches")
	}
}

// narrowByInitials tries to break a tie using the middle initials that
// nameParts discards, and falls back to an ambiguous match when it cannot.
//
// The initials are dropped before comparison because "Jane A Doe" and "Jane
// Doe" should still match each other — but that also makes "Jane A Doe" and
// "Jane B Doe" indistinguishable, which is the tie this recovers.
//
// An initial narrows; it never promotes. A single survivor becomes
// MatchNeedsConfirm, not MatchExact: one letter agreeing is weaker evidence
// than a whole name agreeing, and the instructor still confirms. If the LMS row
// has no initials, or more than one candidate shares them, the tie stands.
func narrowByInitials(row RosterRow, tied []Candidate, reason string) Match {
	want := initialsOf(row.Name)
	if len(want) == 0 {
		return ambiguous(row, tied, reason)
	}

	var survivors []Candidate
	for _, c := range tied {
		if c.FullName == "" {
			continue
		}
		if sameInitials(want, initialsOf(c.FullName)) {
			survivors = append(survivors, c)
		}
	}
	if len(survivors) != 1 {
		// Zero survivors means the initials contradict every candidate, which is
		// information the instructor should see rather than a reason to guess;
		// more than one means the initial did not discriminate.
		return ambiguous(row, tied, reason)
	}
	return Match{
		Row:      row,
		Username: survivors[0].Username,
		Status:   MatchNeedsConfirm,
		Why:      reason + ", narrowed by initial — confirm before use",
	}
}

// initialsOf returns the single-letter name parts, lowercased, in order. These
// are exactly the tokens nameParts throws away.
func initialsOf(name string) []string {
	s := strings.ReplaceAll(normalizeName(name), ",", " ")
	var out []string
	for _, f := range strings.Fields(s) {
		f = strings.TrimSuffix(f, ".")
		if len(f) == 1 {
			out = append(out, f)
		}
	}
	return out
}

func sameInitials(a, b []string) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ambiguous builds a resolvable tie. The candidates are sorted by username so
// the order the instructor sees is stable between runs — a prompt that
// renumbers its options each time is a prompt people mis-answer.
func ambiguous(row RosterRow, tied []Candidate, reason string) Match {
	sorted := append([]Candidate(nil), tied...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Username < sorted[j].Username })
	names := make([]string, 0, len(sorted))
	for _, c := range sorted {
		names = append(names, c.Username)
	}
	return Match{
		Row:        row,
		Status:     MatchAmbiguous,
		Candidates: sorted,
		Why:        reason + " — choose between " + strings.Join(names, ", "),
	}
}

// sameNameParts reports whether two names contain the same set of parts, which
// catches "Last, First" vs "First Last" and dropped middle names.
func sameNameParts(a, b string) bool {
	pa, pb := nameParts(a), nameParts(b)
	if len(pa) == 0 || len(pb) == 0 {
		return false
	}
	// Every part of the shorter name must appear in the longer one.
	if len(pa) > len(pb) {
		pa, pb = pb, pa
	}
	set := make(map[string]bool, len(pb))
	for _, p := range pb {
		set[p] = true
	}
	for _, p := range pa {
		if !set[p] {
			return false
		}
	}
	return true
}

func nameParts(s string) []string {
	s = strings.ReplaceAll(normalizeName(s), ",", " ")
	fields := strings.Fields(s)
	out := fields[:0]
	for _, f := range fields {
		if len(f) > 1 { // drop middle initials
			out = append(out, f)
		}
	}
	return out
}

var stripDiacritics = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

func normalizeName(s string) string {
	folded := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(s)), " "))
	if out, _, err := transform.String(stripDiacritics, folded); err == nil {
		return out
	}
	// Malformed input: a roster row that fails to fold is worse than one
	// that's merely unfolded, so fall back rather than failing the match.
	return folded
}

// squash removes separators so "jane.doe", "jane-doe" and "janedoe" compare equal.
func squash(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r != '.' && r != '-' && r != '_' && r != ' ' && r != ',' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func emailLocalPart(email string) string {
	at := strings.IndexByte(email, '@')
	if at <= 0 {
		return ""
	}
	return email[:at]
}

// BulkEntry is one row of CC-CA6's POST /classrooms/{id}/roster/bulk payload.
// The field names and JSON tags mirror that endpoint exactly.
type BulkEntry struct {
	Username  string `json:"username"`
	EmailHash string `json:"email_hash,omitempty"`
}

// BulkRequest is that endpoint's request body.
type BulkRequest struct {
	Entries []BulkEntry `json:"entries"`
}

// HashEmail reduces an address to the salted digest store.RosterEntry.EmailHash
// documents. It must stay byte-identical to the dashboard's client-side hashing
// in web/src/roster-parse.ts, so a roster half-entered by hand and half by this
// agent produces the same digest for the same student:
//
//	sha256hex( classroomID + ":" + lowercase(email) )
//
// Salting with the classroom ID keeps a digest from acting as a cross-classroom
// identifier for the same person.
func HashEmail(classroomID, email string) string {
	sum := sha256.Sum256([]byte(classroomID + ":" + strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(sum[:])
}

// BuildPayload turns accepted matches into the bulk request. Only usernames and
// hashed emails cross this boundary — it is the last point at which a name or a
// plaintext address exists in the process.
//
// Rows without a username — unmatched, a declined confirmation, or an ambiguity
// the instructor did not resolve — are skipped and returned separately so the
// caller can report them rather than silently shipping a partial roster. The
// test suite pins this for every status that can leave Username empty.
func BuildPayload(classroomID string, matches []Match) (BulkRequest, []Match) {
	req := BulkRequest{}
	var skipped []Match
	for _, m := range matches {
		if m.Username == "" {
			skipped = append(skipped, m)
			continue
		}
		e := BulkEntry{Username: m.Username}
		if m.Row.Email != "" {
			e.EmailHash = HashEmail(classroomID, m.Row.Email)
		}
		req.Entries = append(req.Entries, e)
	}
	return req, skipped
}

// ErrNoConnector is returned when no connector exists for a named LMS. Its
// message points at the manual path rather than dead-ending, per CC-CA7 §5.
type ErrNoConnector struct{ LMS string }

func (e ErrNoConnector) Error() string {
	return fmt.Sprintf(
		"no roster connector for %q yet.\n\n"+
			"Supported today: brightspace (see --help).\n\n"+
			"Use manual bulk entry instead — it always works, whatever the LMS:\n"+
			"  1. Export or copy your roster (one username per line, or username,email)\n"+
			"  2. Open the classroom in Cairn's dashboard → Roster → \"Bulk add\"\n"+
			"     (or POST /classrooms/<id>/roster/bulk)\n"+
			"Emails are hashed in the browser; the address is never sent.",
		e.LMS)
}
