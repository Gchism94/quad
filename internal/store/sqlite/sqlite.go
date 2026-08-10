// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sqlite implements store.Store on top of modernc.org/sqlite (pure Go,
// no cgo). It is the default durable store: a single .db file, no daemon, no
// install friction. Postgres remains the scale-out option.
//
// Differences from the Postgres implementation:
//   - Positional placeholders are ? (SQLite) instead of $N (Postgres).
//   - TIMESTAMPTZ → TEXT stored as RFC3339Nano in UTC.
//   - JSONB/BYTEA → TEXT/BLOB.
//   - FOR UPDATE SKIP LOCKED is unavailable; SetMaxOpenConns(1) makes every
//     write serialized through a single connection, so ClaimNextJob is safe
//     with a plain BEGIN IMMEDIATE transaction.
//   - ON CONFLICT (key) DO NOTHING → INSERT OR IGNORE INTO.
//
// PRIVACY: no column stores a student's legal name, SIS ID, or plaintext email.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

// Store is a SQLite-backed store.Store.
type Store struct {
	db *sql.DB
}

// compile-time guarantee.
var _ store.Store = (*Store)(nil)

// Open opens (or creates) a SQLite database at path, applies the embedded
// schema, and returns a ready Store.
func Open(path string) (*Store, error) {
	// Enable WAL mode, busy timeout, and foreign keys via DSN parameters. This
	// driver (modernc.org/sqlite) only recognizes _pragma=name(value) — the
	// mattn/go-sqlite3-style _journal_mode=/_busy_timeout=/_foreign_keys= this
	// used to say are silently ignored as unrecognized query parameters, so
	// none of the three were ever actually applied (CC-CA15 caught this via a
	// roster-entry-deletion cascade test that should have cascaded and didn't).
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	// Single writer — SQLite's global write lock makes ClaimNextJob correct.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: migrate: %w", err)
	}
	return s, nil
}

// schema is the SQLite-adapted version of the Postgres 0001_init schema plus
// the last_error column added in migration 0002.
const schema = `
CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    host          TEXT NOT NULL,
    host_user_id  TEXT NOT NULL,
    host_username TEXT NOT NULL,
    email         TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    UNIQUE (host, host_user_id)
);

CREATE TABLE IF NOT EXISTS classrooms (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    host           TEXT NOT NULL,
    host_namespace TEXT NOT NULL,
    join_policy    TEXT NOT NULL DEFAULT 'open',
    created_by     TEXT REFERENCES users(id),
    created_at     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS assignments (
    id            TEXT PRIMARY KEY,
    classroom_id  TEXT NOT NULL REFERENCES classrooms(id) ON DELETE CASCADE,
    title         TEXT NOT NULL,
    slug          TEXT NOT NULL,
    template_host TEXT NOT NULL,
    template_ns   TEXT NOT NULL,
    template_name TEXT NOT NULL,
    template_ref  TEXT NOT NULL DEFAULT '',
    type          TEXT NOT NULL DEFAULT 'individual',
    deadline      TEXT,
    grading_spec  TEXT NOT NULL DEFAULT 'grading.json',
    access_policy TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    UNIQUE (classroom_id, slug)
);

CREATE TABLE IF NOT EXISTS roster_entries (
    id            TEXT PRIMARY KEY,
    classroom_id  TEXT NOT NULL REFERENCES classrooms(id) ON DELETE CASCADE,
    host          TEXT NOT NULL,
    host_username TEXT NOT NULL,
    email_hash    TEXT,
    status        TEXT NOT NULL DEFAULT 'invited',
    claimed_at    TEXT,
    UNIQUE (classroom_id, host, host_username)
);

CREATE TABLE IF NOT EXISTS submissions (
    id               TEXT PRIMARY KEY,
    assignment_id    TEXT NOT NULL REFERENCES assignments(id) ON DELETE CASCADE,
    roster_entry_id  TEXT NOT NULL REFERENCES roster_entries(id) ON DELETE CASCADE,
    repo_host        TEXT NOT NULL DEFAULT '',
    repo_namespace   TEXT NOT NULL DEFAULT '',
    repo_name        TEXT NOT NULL DEFAULT '',
    latest_commit    TEXT NOT NULL DEFAULT '',
    last_activity_at TEXT,
    status           TEXT NOT NULL DEFAULT '',
    last_error       TEXT NOT NULL DEFAULT '',
    UNIQUE (assignment_id, roster_entry_id)
);

CREATE TABLE IF NOT EXISTS grades (
    id                  TEXT PRIMARY KEY,
    submission_id       TEXT NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
    score               REAL NOT NULL,
    max_score           REAL NOT NULL,
    breakdown           BLOB,
    run_id              TEXT NOT NULL DEFAULT '',
    graded_at           TEXT NOT NULL,
    export_confirmed_at TEXT
);

CREATE TABLE IF NOT EXISTS provisioning_jobs (
    id              TEXT PRIMARY KEY,
    type            TEXT NOT NULL,
    target_ref      TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    attempts        INTEGER NOT NULL DEFAULT 0,
    idempotency_key TEXT NOT NULL UNIQUE,
    last_error      TEXT NOT NULL DEFAULT '',
    scheduled_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS grading_runs (
    id            TEXT PRIMARY KEY,
    submission_id TEXT NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
    status        TEXT NOT NULL DEFAULT 'pending',
    runner        TEXT NOT NULL DEFAULT '',
    started_at    TEXT,
    finished_at   TEXT,
    result        BLOB,
    logs_ref      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_roster_classroom      ON roster_entries (classroom_id);
CREATE INDEX IF NOT EXISTS idx_roster_host_username  ON roster_entries (host, host_username);
CREATE INDEX IF NOT EXISTS idx_assignments_classroom ON assignments (classroom_id);
CREATE INDEX IF NOT EXISTS idx_submissions_assignment ON submissions (assignment_id);
CREATE INDEX IF NOT EXISTS idx_submissions_repo       ON submissions (repo_host, repo_namespace, repo_name);
CREATE INDEX IF NOT EXISTS idx_jobs_status_scheduled ON provisioning_jobs (status, scheduled_at);
`

func (s *Store) migrate(ctx context.Context) error {
	for _, stmt := range splitStmts(schema) {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("statement %q: %w", stmt[:min(len(stmt), 60)], err)
		}
	}
	return nil
}

func splitStmts(sql string) []string {
	var out []string
	for _, raw := range strings.Split(sql, ";") {
		if s := strings.TrimSpace(raw); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- time helpers ---------------------------------------------------------

const timeFmt = time.RFC3339Nano

func encodeTime(t time.Time) string {
	return t.UTC().Format(timeFmt)
}

func decodeTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(timeFmt, s)
}

func encodeTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := encodeTime(*t)
	return &s
}

func decodeTimePtr(s *string) (*time.Time, error) {
	if s == nil {
		return nil, nil
	}
	t, err := decodeTime(*s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func nowStr() string { return encodeTime(time.Now()) }

// isUnique checks for SQLite unique-constraint errors.
func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func mapErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	return err
}

type rowScanner interface{ Scan(dest ...any) error }

// --- users ----------------------------------------------------------------

const userCols = "id, host, host_user_id, host_username, email, created_at"

func scanUser(r rowScanner, u *store.User) error {
	var createdAt string
	if err := r.Scan(&u.ID, &u.Host, &u.HostUserID, &u.HostUsername, &u.Email, &createdAt); err != nil {
		return err
	}
	t, err := decodeTime(createdAt)
	if err != nil {
		return err
	}
	u.CreatedAt = t
	return nil
}

func (s *Store) CreateUser(ctx context.Context, u *store.User) error {
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (`+userCols+`) VALUES (?,?,?,?,?,?)`,
		u.ID, string(u.Host), u.HostUserID, u.HostUsername, u.Email, encodeTime(u.CreatedAt))
	return err
}

func (s *Store) GetUser(ctx context.Context, id string) (*store.User, error) {
	u := &store.User{}
	if err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, id), u); err != nil {
		return nil, mapErr(err)
	}
	return u, nil
}

func (s *Store) FindUserByHostUsername(ctx context.Context, host adapter.Host, username string) (*store.User, error) {
	u := &store.User{}
	err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userCols+` FROM users WHERE host=? AND host_username=?`, string(host), username), u)
	if err != nil {
		return nil, mapErr(err)
	}
	return u, nil
}

func (s *Store) FindUserByHostUserID(ctx context.Context, host adapter.Host, hostUserID string) (*store.User, error) {
	u := &store.User{}
	err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userCols+` FROM users WHERE host=? AND host_user_id=?`, string(host), hostUserID), u)
	if err != nil {
		return nil, mapErr(err)
	}
	return u, nil
}

// --- classrooms -----------------------------------------------------------

const classroomCols = "id, name, host, host_namespace, join_policy, created_by, created_at"

func scanClassroom(r rowScanner, c *store.Classroom) error {
	var createdBy *string
	var createdAt string
	if err := r.Scan(&c.ID, &c.Name, &c.Host, &c.HostNamespace, &c.JoinPolicy, &createdBy, &createdAt); err != nil {
		return err
	}
	if createdBy != nil {
		c.CreatedBy = *createdBy
	}
	t, err := decodeTime(createdAt)
	if err != nil {
		return err
	}
	c.CreatedAt = t
	return nil
}

func (s *Store) CreateClassroom(ctx context.Context, c *store.Classroom) error {
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	if c.JoinPolicy == "" {
		c.JoinPolicy = store.ClassroomJoinPolicyOpen
	}
	var createdBy *string
	if c.CreatedBy != "" {
		createdBy = &c.CreatedBy
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO classrooms (`+classroomCols+`) VALUES (?,?,?,?,?,?,?)`,
		c.ID, c.Name, string(c.Host), c.HostNamespace, c.JoinPolicy, createdBy, encodeTime(c.CreatedAt))
	return err
}

func (s *Store) GetClassroom(ctx context.Context, id string) (*store.Classroom, error) {
	c := &store.Classroom{}
	if err := scanClassroom(s.db.QueryRowContext(ctx, `SELECT `+classroomCols+` FROM classrooms WHERE id=?`, id), c); err != nil {
		return nil, mapErr(err)
	}
	return c, nil
}

func (s *Store) ListClassrooms(ctx context.Context) ([]*store.Classroom, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+classroomCols+` FROM classrooms ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.Classroom
	for rows.Next() {
		c := &store.Classroom{}
		if err := scanClassroom(rows, c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// --- assignments ----------------------------------------------------------

const assignmentCols = "id, classroom_id, title, slug, template_host, template_ns, template_name, template_ref, type, deadline, grading_spec, access_policy, created_at"

func scanAssignment(r rowScanner, a *store.Assignment) error {
	var deadline *string
	var createdAt string
	err := r.Scan(&a.ID, &a.ClassroomID, &a.Title, &a.Slug,
		&a.TemplateRef.Host, &a.TemplateRef.Namespace, &a.TemplateRef.Name, &a.TemplateRef.Ref,
		&a.Type, &deadline, &a.GradingSpec, &a.AccessPolicy, &createdAt)
	if err != nil {
		return err
	}
	dl, err := decodeTimePtr(deadline)
	if err != nil {
		return err
	}
	a.Deadline = dl
	t, err := decodeTime(createdAt)
	if err != nil {
		return err
	}
	a.CreatedAt = t
	return nil
}

func (s *Store) CreateAssignment(ctx context.Context, a *store.Assignment) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	if a.Type == "" {
		a.Type = store.AssignmentIndividual
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO assignments (`+assignmentCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.ClassroomID, a.Title, a.Slug,
		string(a.TemplateRef.Host), a.TemplateRef.Namespace, a.TemplateRef.Name, a.TemplateRef.Ref,
		string(a.Type), encodeTimePtr(a.Deadline), a.GradingSpec, a.AccessPolicy, encodeTime(a.CreatedAt))
	return err
}

func (s *Store) GetAssignment(ctx context.Context, id string) (*store.Assignment, error) {
	a := &store.Assignment{}
	if err := scanAssignment(s.db.QueryRowContext(ctx, `SELECT `+assignmentCols+` FROM assignments WHERE id=?`, id), a); err != nil {
		return nil, mapErr(err)
	}
	return a, nil
}

func (s *Store) UpdateAssignment(ctx context.Context, a *store.Assignment) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE assignments SET title=?, slug=?, template_host=?, template_ns=?, template_name=?,
		   template_ref=?, type=?, deadline=?, grading_spec=?, access_policy=?
		 WHERE id=?`,
		a.Title, a.Slug, string(a.TemplateRef.Host), a.TemplateRef.Namespace,
		a.TemplateRef.Name, a.TemplateRef.Ref, string(a.Type), encodeTimePtr(a.Deadline), a.GradingSpec, a.AccessPolicy, a.ID)
	return affected(res, err)
}

func (s *Store) ListAssignmentsByClassroom(ctx context.Context, classroomID string) ([]*store.Assignment, error) {
	return s.queryAssignments(ctx, `SELECT `+assignmentCols+` FROM assignments WHERE classroom_id=? ORDER BY created_at`, classroomID)
}

func (s *Store) ListAssignmentsDueBy(ctx context.Context, t time.Time) ([]*store.Assignment, error) {
	return s.queryAssignments(ctx,
		`SELECT `+assignmentCols+` FROM assignments WHERE deadline IS NOT NULL AND deadline <= ? ORDER BY deadline`,
		encodeTime(t))
}

func (s *Store) queryAssignments(ctx context.Context, q string, args ...any) ([]*store.Assignment, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.Assignment
	for rows.Next() {
		a := &store.Assignment{}
		if err := scanAssignment(rows, a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// --- roster ---------------------------------------------------------------

const rosterCols = "id, classroom_id, host, host_username, email_hash, status, claimed_at"

func scanRoster(r rowScanner, e *store.RosterEntry) error {
	var emailHash *string
	var claimedAt *string
	if err := r.Scan(&e.ID, &e.ClassroomID, &e.Host, &e.HostUsername, &emailHash, &e.Status, &claimedAt); err != nil {
		return err
	}
	if emailHash != nil {
		e.EmailHash = *emailHash
	}
	ca, err := decodeTimePtr(claimedAt)
	if err != nil {
		return err
	}
	e.ClaimedAt = ca
	return nil
}

func (s *Store) CreateRosterEntry(ctx context.Context, e *store.RosterEntry) error {
	if e.Status == "" {
		e.Status = store.RosterInvited
	}
	var emailHash *string
	if e.EmailHash != "" {
		emailHash = &e.EmailHash
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO roster_entries (`+rosterCols+`) VALUES (?,?,?,?,?,?,?)`,
		e.ID, e.ClassroomID, string(e.Host), e.HostUsername, emailHash, string(e.Status), encodeTimePtr(e.ClaimedAt))
	return err
}

func (s *Store) GetRosterEntry(ctx context.Context, id string) (*store.RosterEntry, error) {
	e := &store.RosterEntry{}
	if err := scanRoster(s.db.QueryRowContext(ctx, `SELECT `+rosterCols+` FROM roster_entries WHERE id=?`, id), e); err != nil {
		return nil, mapErr(err)
	}
	return e, nil
}

func (s *Store) FindRosterEntryByUsername(ctx context.Context, classroomID, username string) (*store.RosterEntry, error) {
	e := &store.RosterEntry{}
	err := scanRoster(s.db.QueryRowContext(ctx,
		`SELECT `+rosterCols+` FROM roster_entries WHERE classroom_id=? AND host_username=?`, classroomID, username), e)
	if err != nil {
		return nil, mapErr(err)
	}
	return e, nil
}

func (s *Store) UpdateRosterEntry(ctx context.Context, e *store.RosterEntry) error {
	var emailHash *string
	if e.EmailHash != "" {
		emailHash = &e.EmailHash
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE roster_entries SET host=?, host_username=?, email_hash=?, status=?, claimed_at=? WHERE id=?`,
		string(e.Host), e.HostUsername, emailHash, string(e.Status), encodeTimePtr(e.ClaimedAt), e.ID)
	return affected(res, err)
}

func (s *Store) ListRosterEntries(ctx context.Context, classroomID string) ([]*store.RosterEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+rosterCols+` FROM roster_entries WHERE classroom_id=? ORDER BY host_username`, classroomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.RosterEntry
	for rows.Next() {
		e := &store.RosterEntry{}
		if err := scanRoster(rows, e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteRosterEntry relies on the DSN's _foreign_keys=on and the schema's ON
// DELETE CASCADE (roster_entries -> submissions -> grades/grading_runs) to
// remove every dependent row.
func (s *Store) DeleteRosterEntry(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM roster_entries WHERE id=?`, id)
	return affected(res, err)
}

// --- submissions ----------------------------------------------------------

const submissionCols = "id, assignment_id, roster_entry_id, repo_host, repo_namespace, repo_name, latest_commit, last_activity_at, status, last_error"

func scanSubmission(r rowScanner, sub *store.Submission) error {
	var lastActivity *string
	if err := r.Scan(&sub.ID, &sub.AssignmentID, &sub.RosterEntryID,
		&sub.Repo.Host, &sub.Repo.Namespace, &sub.Repo.Name,
		&sub.LatestCommit, &lastActivity, &sub.Status, &sub.LastError); err != nil {
		return err
	}
	la, err := decodeTimePtr(lastActivity)
	if err != nil {
		return err
	}
	sub.LastActivityAt = la
	return nil
}

func (s *Store) CreateSubmission(ctx context.Context, sub *store.Submission) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO submissions (`+submissionCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		sub.ID, sub.AssignmentID, sub.RosterEntryID,
		string(sub.Repo.Host), sub.Repo.Namespace, sub.Repo.Name,
		sub.LatestCommit, encodeTimePtr(sub.LastActivityAt), sub.Status, sub.LastError)
	if isUnique(err) {
		return store.ErrConflict
	}
	return err
}

func (s *Store) GetSubmission(ctx context.Context, id string) (*store.Submission, error) {
	sub := &store.Submission{}
	if err := scanSubmission(s.db.QueryRowContext(ctx, `SELECT `+submissionCols+` FROM submissions WHERE id=?`, id), sub); err != nil {
		return nil, mapErr(err)
	}
	return sub, nil
}

func (s *Store) FindSubmission(ctx context.Context, assignmentID, rosterEntryID string) (*store.Submission, error) {
	sub := &store.Submission{}
	err := scanSubmission(s.db.QueryRowContext(ctx,
		`SELECT `+submissionCols+` FROM submissions WHERE assignment_id=? AND roster_entry_id=?`, assignmentID, rosterEntryID), sub)
	if err != nil {
		return nil, mapErr(err)
	}
	return sub, nil
}

func (s *Store) FindSubmissionByRepo(ctx context.Context, host adapter.Host, namespace, name string) (*store.Submission, error) {
	sub := &store.Submission{}
	err := scanSubmission(s.db.QueryRowContext(ctx,
		`SELECT `+submissionCols+` FROM submissions WHERE repo_host=? AND repo_namespace=? AND repo_name=?`,
		string(host), namespace, name), sub)
	if err != nil {
		return nil, mapErr(err)
	}
	return sub, nil
}

func (s *Store) ListSubmissionsByRosterUsername(ctx context.Context, host adapter.Host, username string) ([]*store.Submission, error) {
	// Join roster entries on (host, host_username) — the only identity anchor.
	// NULL last_activity_at sorts last (newest activity first).
	return s.querySubmissions(ctx,
		`SELECT sub.id, sub.assignment_id, sub.roster_entry_id, sub.repo_host, sub.repo_namespace, sub.repo_name,
		        sub.latest_commit, sub.last_activity_at, sub.status, sub.last_error
		 FROM submissions sub
		 JOIN roster_entries r ON r.id = sub.roster_entry_id
		 WHERE r.host=? AND r.host_username=?
		 ORDER BY sub.last_activity_at IS NULL, sub.last_activity_at DESC, sub.id`,
		string(host), username)
}

func (s *Store) UpdateSubmission(ctx context.Context, sub *store.Submission) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE submissions SET repo_host=?, repo_namespace=?, repo_name=?, latest_commit=?, last_activity_at=?, status=?, last_error=? WHERE id=?`,
		string(sub.Repo.Host), sub.Repo.Namespace, sub.Repo.Name,
		sub.LatestCommit, encodeTimePtr(sub.LastActivityAt), sub.Status, sub.LastError, sub.ID)
	return affected(res, err)
}

func (s *Store) ListSubmissionsByAssignment(ctx context.Context, assignmentID string) ([]*store.Submission, error) {
	return s.querySubmissions(ctx, `SELECT `+submissionCols+` FROM submissions WHERE assignment_id=? ORDER BY id`, assignmentID)
}

func (s *Store) ListSubmissionsByClassroom(ctx context.Context, classroomID string) ([]*store.Submission, error) {
	return s.querySubmissions(ctx,
		`SELECT sub.id, sub.assignment_id, sub.roster_entry_id, sub.repo_host, sub.repo_namespace, sub.repo_name,
		        sub.latest_commit, sub.last_activity_at, sub.status, sub.last_error
		 FROM submissions sub
		 JOIN assignments a ON a.id = sub.assignment_id
		 WHERE a.classroom_id=?
		 ORDER BY sub.id`, classroomID)
}

func (s *Store) querySubmissions(ctx context.Context, q string, args ...any) ([]*store.Submission, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.Submission
	for rows.Next() {
		sub := &store.Submission{}
		if err := scanSubmission(rows, sub); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// --- grades ---------------------------------------------------------------

const gradeCols = "id, submission_id, score, max_score, breakdown, run_id, graded_at, export_confirmed_at"

func (s *Store) CreateGrade(ctx context.Context, g *store.Grade) error {
	if g.GradedAt.IsZero() {
		g.GradedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO grades (`+gradeCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		g.ID, g.SubmissionID, g.Score, g.MaxScore, g.Breakdown, g.RunID, encodeTime(g.GradedAt), encodeTimePtr(g.ExportConfirmedAt))
	return err
}

func scanGrade(r rowScanner, g *store.Grade) error {
	var gradedAt string
	var exportConfirmedAt *string
	if err := r.Scan(&g.ID, &g.SubmissionID, &g.Score, &g.MaxScore, &g.Breakdown, &g.RunID, &gradedAt, &exportConfirmedAt); err != nil {
		return err
	}
	t, err := decodeTime(gradedAt)
	if err != nil {
		return err
	}
	g.GradedAt = t
	eca, err := decodeTimePtr(exportConfirmedAt)
	if err != nil {
		return err
	}
	g.ExportConfirmedAt = eca
	return nil
}

func (s *Store) LatestGradeForSubmission(ctx context.Context, submissionID string) (*store.Grade, error) {
	g := &store.Grade{}
	err := scanGrade(s.db.QueryRowContext(ctx,
		`SELECT `+gradeCols+` FROM grades WHERE submission_id=? ORDER BY graded_at DESC LIMIT 1`, submissionID), g)
	if err != nil {
		return nil, mapErr(err)
	}
	return g, nil
}

func (s *Store) ListGradesBySubmission(ctx context.Context, submissionID string) ([]*store.Grade, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+gradeCols+` FROM grades WHERE submission_id=? ORDER BY graded_at DESC, id`, submissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.Grade
	for rows.Next() {
		g := &store.Grade{}
		if err := scanGrade(rows, g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ConfirmExport marks export_confirmed_at on every not-yet-confirmed grade
// belonging to classroomID. Deliberately a separate action from grades.csv
// itself — see Grade.ExportConfirmedAt's doc comment.
func (s *Store) ConfirmExport(ctx context.Context, classroomID string, now time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE grades SET export_confirmed_at=?
		 WHERE export_confirmed_at IS NULL
		   AND submission_id IN (
		       SELECT sub.id FROM submissions sub
		       JOIN assignments a ON a.id = sub.assignment_id
		       WHERE a.classroom_id=?
		   )`,
		encodeTime(now), classroomID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// PurgeExportedGrades deletes every grade whose export was confirmed strictly
// before cutoff, and — for each run_id it frees — the matching grading_runs
// row, but only when no surviving grade still references that run.
// grades.run_id is a loose TEXT column, not a foreign key, so that check is
// done explicitly rather than relying on a cascade.
func (s *Store) PurgeExportedGrades(ctx context.Context, cutoff time.Time) (int, int, error) {
	rows, err := s.db.QueryContext(ctx,
		`DELETE FROM grades WHERE export_confirmed_at IS NOT NULL AND export_confirmed_at < ? RETURNING run_id`,
		encodeTime(cutoff))
	if err != nil {
		return 0, 0, err
	}
	var runIDs []string
	gradesPurged := 0
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			rows.Close()
			return gradesPurged, 0, err
		}
		gradesPurged++
		if runID != "" {
			runIDs = append(runIDs, runID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return gradesPurged, 0, err
	}
	rows.Close()

	runsPurged := 0
	for _, runID := range runIDs {
		var stillReferenced int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM grades WHERE run_id=?`, runID).Scan(&stillReferenced); err != nil {
			return gradesPurged, runsPurged, err
		}
		if stillReferenced > 0 {
			continue
		}
		res, err := s.db.ExecContext(ctx, `DELETE FROM grading_runs WHERE id=?`, runID)
		if err != nil {
			return gradesPurged, runsPurged, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			runsPurged++
		}
	}
	return gradesPurged, runsPurged, nil
}

// --- grading runs ---------------------------------------------------------

const runCols = "id, submission_id, status, runner, started_at, finished_at, result, logs_ref"

func scanRun(r rowScanner, run *store.GradingRun) error {
	var startedAt, finishedAt *string
	if err := r.Scan(&run.ID, &run.SubmissionID, &run.Status, &run.Runner,
		&startedAt, &finishedAt, &run.Result, &run.LogsRef); err != nil {
		return err
	}
	sa, err := decodeTimePtr(startedAt)
	if err != nil {
		return err
	}
	fa, err := decodeTimePtr(finishedAt)
	if err != nil {
		return err
	}
	run.StartedAt = sa
	run.FinishedAt = fa
	return nil
}

func (s *Store) CreateGradingRun(ctx context.Context, run *store.GradingRun) error {
	if run.Status == "" {
		run.Status = "pending"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO grading_runs (`+runCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		run.ID, run.SubmissionID, run.Status, run.Runner,
		encodeTimePtr(run.StartedAt), encodeTimePtr(run.FinishedAt), run.Result, run.LogsRef)
	return err
}

func (s *Store) UpdateGradingRun(ctx context.Context, run *store.GradingRun) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE grading_runs SET status=?, runner=?, started_at=?, finished_at=?, result=?, logs_ref=? WHERE id=?`,
		run.Status, run.Runner, encodeTimePtr(run.StartedAt), encodeTimePtr(run.FinishedAt), run.Result, run.LogsRef, run.ID)
	return affected(res, err)
}

func (s *Store) ListGradingRunsBySubmission(ctx context.Context, submissionID string) ([]*store.GradingRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runCols+` FROM grading_runs WHERE submission_id=? ORDER BY started_at`, submissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.GradingRun
	for rows.Next() {
		run := &store.GradingRun{}
		if err := scanRun(rows, run); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// --- provisioning jobs ----------------------------------------------------

const jobCols = "id, type, target_ref, status, attempts, idempotency_key, last_error, scheduled_at"

func scanJob(r rowScanner, j *store.ProvisioningJob) error {
	var scheduledAt string
	if err := r.Scan(&j.ID, &j.Type, &j.TargetRef, &j.Status, &j.Attempts,
		&j.IdempotencyKey, &j.LastError, &scheduledAt); err != nil {
		return err
	}
	t, err := decodeTime(scheduledAt)
	if err != nil {
		return err
	}
	j.ScheduledAt = t
	return nil
}

// CreateJob is idempotent on idempotency_key: a duplicate returns created=false.
func (s *Store) CreateJob(ctx context.Context, j *store.ProvisioningJob) (bool, error) {
	if j.Status == "" {
		j.Status = store.JobPending
	}
	if j.ScheduledAt.IsZero() {
		j.ScheduledAt = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO provisioning_jobs (`+jobCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		j.ID, j.Type, j.TargetRef, string(j.Status), j.Attempts, j.IdempotencyKey, j.LastError, encodeTime(j.ScheduledAt))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ClaimNextJob atomically takes the oldest runnable pending job and marks it
// in-progress. With SetMaxOpenConns(1) the single writer makes this safe
// without advisory locks.
func (s *Store) ClaimNextJob(ctx context.Context) (*store.ProvisioningJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var id string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM provisioning_jobs
		 WHERE status='pending' AND scheduled_at <= ?
		 ORDER BY scheduled_at ASC LIMIT 1`,
		encodeTime(time.Now().UTC())).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE provisioning_jobs SET status='in_progress' WHERE id=?`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	j := &store.ProvisioningJob{}
	err = scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM provisioning_jobs WHERE id=?`, id), j)
	if err != nil {
		return nil, err
	}
	return j, nil
}

func (s *Store) UpdateJob(ctx context.Context, j *store.ProvisioningJob) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE provisioning_jobs SET status=?, attempts=?, last_error=?, scheduled_at=? WHERE id=?`,
		string(j.Status), j.Attempts, j.LastError, encodeTime(j.ScheduledAt), j.ID)
	return affected(res, err)
}

// Close releases the database connection pool.
func (s *Store) Close() error { return s.db.Close() }

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}
