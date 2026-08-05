// SPDX-License-Identifier: AGPL-3.0-or-later

// Package postgres implements store.Store on top of database/sql.
//
// It imports NO SQL driver: the caller opens a *sql.DB with whatever driver they
// prefer (the cmd wires github.com/jackc/pgx/v5/stdlib behind the `postgres`
// build tag) and passes it to New. That keeps this package — and the default
// build of the whole module — free of external dependencies, while the
// production database layer is real and driver-backed.
//
// Queries use PostgreSQL positional placeholders ($1, $2, …) and PostgreSQL
// features (ON CONFLICT, FOR UPDATE SKIP LOCKED, JSONB), so a PostgreSQL-family
// database is required.
//
// PRIVACY: every column written here is defined by migrations/0001_init — there
// is no student name, SIS ID, or plaintext email anywhere in the schema.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/internal/store/migrations"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

// Store is a PostgreSQL-backed store.Store.
type Store struct {
	db *sql.DB
}

// New wraps an open *sql.DB. The caller owns the DB's lifecycle (and driver).
func New(db *sql.DB) *Store { return &Store{db: db} }

// compile-time guarantee that every interface method is implemented.
var _ store.Store = (*Store)(nil)

// Migrate applies the embedded schema. The bundled migration uses
// CREATE ... IF NOT EXISTS, so it is safe to run on every startup. Statements
// are executed individually (the database/sql extended protocol does not allow
// multiple statements per Exec); the schema contains no procedural bodies or
// semicolons inside literals, so a simple split is sufficient.
func (s *Store) Migrate(ctx context.Context) error {
	raw, err := migrations.FS.ReadFile("0001_init.up.sql")
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range splitStatements(string(raw)) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply statement: %w", err)
		}
	}
	return tx.Commit()
}

// splitStatements drops full-line SQL comments and blank lines, then splits the
// remaining text into statements on lines ending in ';'.
func splitStatements(sqlText string) []string {
	var out []string
	var b strings.Builder
	for _, line := range strings.Split(sqlText, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
		if strings.HasSuffix(trimmed, ";") {
			if stmt := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(b.String()), ";")); stmt != "" {
				out = append(out, stmt)
			}
			b.Reset()
		}
	}
	if stmt := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(b.String()), ";")); stmt != "" {
		out = append(out, stmt)
	}
	return out
}

// --- small helpers for nullable columns ---

type rowScanner interface{ Scan(dest ...any) error }

func nt(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *t, Valid: true}
}

func tp(n sql.NullTime) *time.Time {
	if !n.Valid {
		return nil
	}
	v := n.Time
	return &v
}

func ns(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// jsonb passes JSON bytes as a query arg, or NULL when empty.
func jsonb(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func mapErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	return err
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint violation
// (SQLSTATE 23505). It relies on the pgx driver exposing a SQLState() method; on
// any other driver it conservatively returns false.
func isUniqueViolation(err error) bool {
	var se interface{ SQLState() string }
	return errors.As(err, &se) && se.SQLState() == "23505"
}

func now(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}

// ============================ users ============================

const userCols = "id, host, host_user_id, host_username, email, created_at"

func scanUser(r rowScanner, u *store.User) error {
	return r.Scan(&u.ID, &u.Host, &u.HostUserID, &u.HostUsername, &u.Email, &u.CreatedAt)
}

func (s *Store) CreateUser(ctx context.Context, u *store.User) error {
	u.CreatedAt = now(u.CreatedAt)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (`+userCols+`) VALUES ($1,$2,$3,$4,$5,$6)`,
		u.ID, string(u.Host), u.HostUserID, u.HostUsername, u.Email, u.CreatedAt)
	return err
}

func (s *Store) GetUser(ctx context.Context, id string) (*store.User, error) {
	u := &store.User{}
	if err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=$1`, id), u); err != nil {
		return nil, mapErr(err)
	}
	return u, nil
}

func (s *Store) FindUserByHostUsername(ctx context.Context, host adapter.Host, username string) (*store.User, error) {
	u := &store.User{}
	err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userCols+` FROM users WHERE host=$1 AND host_username=$2`, string(host), username), u)
	if err != nil {
		return nil, mapErr(err)
	}
	return u, nil
}

func (s *Store) FindUserByHostUserID(ctx context.Context, host adapter.Host, hostUserID string) (*store.User, error) {
	u := &store.User{}
	err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userCols+` FROM users WHERE host=$1 AND host_user_id=$2`, string(host), hostUserID), u)
	if err != nil {
		return nil, mapErr(err)
	}
	return u, nil
}

// ========================= classrooms =========================

const classroomCols = "id, name, host, host_namespace, join_policy, created_by, created_at"

func scanClassroom(r rowScanner, c *store.Classroom) error {
	var createdBy sql.NullString
	if err := r.Scan(&c.ID, &c.Name, &c.Host, &c.HostNamespace, &c.JoinPolicy, &createdBy, &c.CreatedAt); err != nil {
		return err
	}
	c.CreatedBy = createdBy.String
	return nil
}

func (s *Store) CreateClassroom(ctx context.Context, c *store.Classroom) error {
	c.CreatedAt = now(c.CreatedAt)
	if c.JoinPolicy == "" {
		c.JoinPolicy = store.ClassroomJoinPolicyOpen
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO classrooms (`+classroomCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		c.ID, c.Name, string(c.Host), c.HostNamespace, c.JoinPolicy, ns(c.CreatedBy), c.CreatedAt)
	return err
}

func (s *Store) GetClassroom(ctx context.Context, id string) (*store.Classroom, error) {
	c := &store.Classroom{}
	if err := scanClassroom(s.db.QueryRowContext(ctx, `SELECT `+classroomCols+` FROM classrooms WHERE id=$1`, id), c); err != nil {
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

// ========================= assignments =========================

const assignmentCols = "id, classroom_id, title, slug, template_host, template_ns, template_name, template_ref, type, deadline, grading_spec, access_policy, created_at"

func scanAssignment(r rowScanner, a *store.Assignment) error {
	var deadline sql.NullTime
	err := r.Scan(&a.ID, &a.ClassroomID, &a.Title, &a.Slug,
		&a.TemplateRef.Host, &a.TemplateRef.Namespace, &a.TemplateRef.Name, &a.TemplateRef.Ref,
		&a.Type, &deadline, &a.GradingSpec, &a.AccessPolicy, &a.CreatedAt)
	if err != nil {
		return err
	}
	a.Deadline = tp(deadline)
	return nil
}

func (s *Store) CreateAssignment(ctx context.Context, a *store.Assignment) error {
	a.CreatedAt = now(a.CreatedAt)
	if a.Type == "" {
		a.Type = store.AssignmentIndividual
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO assignments (`+assignmentCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		a.ID, a.ClassroomID, a.Title, a.Slug,
		string(a.TemplateRef.Host), a.TemplateRef.Namespace, a.TemplateRef.Name, a.TemplateRef.Ref,
		string(a.Type), nt(a.Deadline), a.GradingSpec, a.AccessPolicy, a.CreatedAt)
	return err
}

func (s *Store) GetAssignment(ctx context.Context, id string) (*store.Assignment, error) {
	a := &store.Assignment{}
	if err := scanAssignment(s.db.QueryRowContext(ctx, `SELECT `+assignmentCols+` FROM assignments WHERE id=$1`, id), a); err != nil {
		return nil, mapErr(err)
	}
	return a, nil
}

func (s *Store) UpdateAssignment(ctx context.Context, a *store.Assignment) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE assignments SET title=$2, slug=$3, template_host=$4, template_ns=$5,
		   template_name=$6, template_ref=$7, type=$8, deadline=$9, grading_spec=$10, access_policy=$11
		 WHERE id=$1`,
		a.ID, a.Title, a.Slug, string(a.TemplateRef.Host), a.TemplateRef.Namespace,
		a.TemplateRef.Name, a.TemplateRef.Ref, string(a.Type), nt(a.Deadline), a.GradingSpec, a.AccessPolicy)
	return affected(res, err)
}

func (s *Store) ListAssignmentsByClassroom(ctx context.Context, classroomID string) ([]*store.Assignment, error) {
	return s.queryAssignments(ctx, `SELECT `+assignmentCols+` FROM assignments WHERE classroom_id=$1 ORDER BY created_at`, classroomID)
}

func (s *Store) ListAssignmentsDueBy(ctx context.Context, t time.Time) ([]*store.Assignment, error) {
	return s.queryAssignments(ctx, `SELECT `+assignmentCols+` FROM assignments WHERE deadline IS NOT NULL AND deadline <= $1 ORDER BY deadline`, t)
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

// ========================= roster =========================

const rosterCols = "id, classroom_id, host, host_username, email_hash, status, claimed_at"

func scanRoster(r rowScanner, e *store.RosterEntry) error {
	var eh sql.NullString
	var claimed sql.NullTime
	if err := r.Scan(&e.ID, &e.ClassroomID, &e.Host, &e.HostUsername, &eh, &e.Status, &claimed); err != nil {
		return err
	}
	e.EmailHash = eh.String
	e.ClaimedAt = tp(claimed)
	return nil
}

func (s *Store) CreateRosterEntry(ctx context.Context, e *store.RosterEntry) error {
	if e.Status == "" {
		e.Status = store.RosterInvited
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO roster_entries (`+rosterCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		e.ID, e.ClassroomID, string(e.Host), e.HostUsername, ns(e.EmailHash), string(e.Status), nt(e.ClaimedAt))
	return err
}

func (s *Store) GetRosterEntry(ctx context.Context, id string) (*store.RosterEntry, error) {
	e := &store.RosterEntry{}
	if err := scanRoster(s.db.QueryRowContext(ctx, `SELECT `+rosterCols+` FROM roster_entries WHERE id=$1`, id), e); err != nil {
		return nil, mapErr(err)
	}
	return e, nil
}

func (s *Store) FindRosterEntryByUsername(ctx context.Context, classroomID, username string) (*store.RosterEntry, error) {
	e := &store.RosterEntry{}
	err := scanRoster(s.db.QueryRowContext(ctx,
		`SELECT `+rosterCols+` FROM roster_entries WHERE classroom_id=$1 AND host_username=$2`, classroomID, username), e)
	if err != nil {
		return nil, mapErr(err)
	}
	return e, nil
}

func (s *Store) UpdateRosterEntry(ctx context.Context, e *store.RosterEntry) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE roster_entries SET host=$2, host_username=$3, email_hash=$4, status=$5, claimed_at=$6 WHERE id=$1`,
		e.ID, string(e.Host), e.HostUsername, ns(e.EmailHash), string(e.Status), nt(e.ClaimedAt))
	return affected(res, err)
}

func (s *Store) ListRosterEntries(ctx context.Context, classroomID string) ([]*store.RosterEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+rosterCols+` FROM roster_entries WHERE classroom_id=$1 ORDER BY host_username`, classroomID)
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

// ========================= submissions =========================

const submissionCols = "id, assignment_id, roster_entry_id, repo_host, repo_namespace, repo_name, latest_commit, last_activity_at, status, last_error"

func scanSubmission(r rowScanner, sub *store.Submission) error {
	var last sql.NullTime
	if err := r.Scan(&sub.ID, &sub.AssignmentID, &sub.RosterEntryID,
		&sub.Repo.Host, &sub.Repo.Namespace, &sub.Repo.Name, &sub.LatestCommit, &last, &sub.Status, &sub.LastError); err != nil {
		return err
	}
	sub.LastActivityAt = tp(last)
	return nil
}

func (s *Store) CreateSubmission(ctx context.Context, sub *store.Submission) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO submissions (`+submissionCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		sub.ID, sub.AssignmentID, sub.RosterEntryID,
		string(sub.Repo.Host), sub.Repo.Namespace, sub.Repo.Name, sub.LatestCommit, nt(sub.LastActivityAt), sub.Status, sub.LastError)
	if isUniqueViolation(err) {
		return store.ErrConflict
	}
	return err
}

func (s *Store) GetSubmission(ctx context.Context, id string) (*store.Submission, error) {
	sub := &store.Submission{}
	if err := scanSubmission(s.db.QueryRowContext(ctx, `SELECT `+submissionCols+` FROM submissions WHERE id=$1`, id), sub); err != nil {
		return nil, mapErr(err)
	}
	return sub, nil
}

func (s *Store) FindSubmission(ctx context.Context, assignmentID, rosterEntryID string) (*store.Submission, error) {
	sub := &store.Submission{}
	err := scanSubmission(s.db.QueryRowContext(ctx,
		`SELECT `+submissionCols+` FROM submissions WHERE assignment_id=$1 AND roster_entry_id=$2`, assignmentID, rosterEntryID), sub)
	if err != nil {
		return nil, mapErr(err)
	}
	return sub, nil
}

func (s *Store) FindSubmissionByRepo(ctx context.Context, host adapter.Host, namespace, name string) (*store.Submission, error) {
	sub := &store.Submission{}
	err := scanSubmission(s.db.QueryRowContext(ctx,
		`SELECT `+submissionCols+` FROM submissions WHERE repo_host=$1 AND repo_namespace=$2 AND repo_name=$3`,
		string(host), namespace, name), sub)
	if err != nil {
		return nil, mapErr(err)
	}
	return sub, nil
}

func (s *Store) ListSubmissionsByRosterUsername(ctx context.Context, host adapter.Host, username string) ([]*store.Submission, error) {
	// Join roster entries on (host, host_username) — the only identity anchor.
	return s.querySubmissions(ctx,
		`SELECT sub.id, sub.assignment_id, sub.roster_entry_id, sub.repo_host, sub.repo_namespace, sub.repo_name,
		        sub.latest_commit, sub.last_activity_at, sub.status, sub.last_error
		 FROM submissions sub
		 JOIN roster_entries r ON r.id = sub.roster_entry_id
		 WHERE r.host=$1 AND r.host_username=$2
		 ORDER BY sub.last_activity_at DESC NULLS LAST, sub.id`,
		string(host), username)
}

func (s *Store) UpdateSubmission(ctx context.Context, sub *store.Submission) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE submissions SET repo_host=$2, repo_namespace=$3, repo_name=$4, latest_commit=$5, last_activity_at=$6, status=$7, last_error=$8 WHERE id=$1`,
		sub.ID, string(sub.Repo.Host), sub.Repo.Namespace, sub.Repo.Name, sub.LatestCommit, nt(sub.LastActivityAt), sub.Status, sub.LastError)
	return affected(res, err)
}

func (s *Store) ListSubmissionsByAssignment(ctx context.Context, assignmentID string) ([]*store.Submission, error) {
	return s.querySubmissions(ctx, `SELECT `+submissionCols+` FROM submissions WHERE assignment_id=$1 ORDER BY id`, assignmentID)
}

func (s *Store) ListSubmissionsByClassroom(ctx context.Context, classroomID string) ([]*store.Submission, error) {
	return s.querySubmissions(ctx,
		`SELECT sub.id, sub.assignment_id, sub.roster_entry_id, sub.repo_host, sub.repo_namespace, sub.repo_name,
		        sub.latest_commit, sub.last_activity_at, sub.status, sub.last_error
		 FROM submissions sub
		 JOIN assignments a ON a.id = sub.assignment_id
		 WHERE a.classroom_id=$1
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

// ========================= grades =========================

const gradeCols = "id, submission_id, score, max_score, breakdown, run_id, graded_at"

func (s *Store) CreateGrade(ctx context.Context, g *store.Grade) error {
	g.GradedAt = now(g.GradedAt)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO grades (`+gradeCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		g.ID, g.SubmissionID, g.Score, g.MaxScore, jsonb(g.Breakdown), g.RunID, g.GradedAt)
	return err
}

func scanGrade(r rowScanner, g *store.Grade) error {
	return r.Scan(&g.ID, &g.SubmissionID, &g.Score, &g.MaxScore, &g.Breakdown, &g.RunID, &g.GradedAt)
}

func (s *Store) LatestGradeForSubmission(ctx context.Context, submissionID string) (*store.Grade, error) {
	g := &store.Grade{}
	err := scanGrade(s.db.QueryRowContext(ctx,
		`SELECT `+gradeCols+` FROM grades WHERE submission_id=$1 ORDER BY graded_at DESC LIMIT 1`, submissionID), g)
	if err != nil {
		return nil, mapErr(err)
	}
	return g, nil
}

func (s *Store) ListGradesBySubmission(ctx context.Context, submissionID string) ([]*store.Grade, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+gradeCols+` FROM grades WHERE submission_id=$1 ORDER BY graded_at DESC, id`, submissionID)
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

// ========================= grading runs =========================

const runCols = "id, submission_id, status, runner, started_at, finished_at, result, logs_ref"

func scanRun(r rowScanner, run *store.GradingRun) error {
	var started, finished sql.NullTime
	if err := r.Scan(&run.ID, &run.SubmissionID, &run.Status, &run.Runner, &started, &finished, &run.Result, &run.LogsRef); err != nil {
		return err
	}
	run.StartedAt = tp(started)
	run.FinishedAt = tp(finished)
	return nil
}

func (s *Store) CreateGradingRun(ctx context.Context, run *store.GradingRun) error {
	if run.Status == "" {
		run.Status = "pending"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO grading_runs (`+runCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		run.ID, run.SubmissionID, run.Status, run.Runner, nt(run.StartedAt), nt(run.FinishedAt), jsonb(run.Result), run.LogsRef)
	return err
}

func (s *Store) UpdateGradingRun(ctx context.Context, run *store.GradingRun) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE grading_runs SET status=$2, runner=$3, started_at=$4, finished_at=$5, result=$6, logs_ref=$7 WHERE id=$1`,
		run.ID, run.Status, run.Runner, nt(run.StartedAt), nt(run.FinishedAt), jsonb(run.Result), run.LogsRef)
	return affected(res, err)
}

func (s *Store) ListGradingRunsBySubmission(ctx context.Context, submissionID string) ([]*store.GradingRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runCols+` FROM grading_runs WHERE submission_id=$1 ORDER BY started_at`, submissionID)
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

// ========================= provisioning jobs =========================

const jobCols = "id, type, target_ref, status, attempts, idempotency_key, last_error, scheduled_at"

func scanJob(r rowScanner, j *store.ProvisioningJob) error {
	return r.Scan(&j.ID, &j.Type, &j.TargetRef, &j.Status, &j.Attempts, &j.IdempotencyKey, &j.LastError, &j.ScheduledAt)
}

// CreateJob is idempotent on idempotency_key; a duplicate returns created=false.
func (s *Store) CreateJob(ctx context.Context, j *store.ProvisioningJob) (bool, error) {
	if j.Status == "" {
		j.Status = store.JobPending
	}
	j.ScheduledAt = now(j.ScheduledAt)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO provisioning_jobs (`+jobCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 ON CONFLICT (idempotency_key) DO NOTHING`,
		j.ID, j.Type, j.TargetRef, string(j.Status), j.Attempts, j.IdempotencyKey, j.LastError, j.ScheduledAt)
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
// in-progress. FOR UPDATE SKIP LOCKED makes it safe to run many workers.
func (s *Store) ClaimNextJob(ctx context.Context) (*store.ProvisioningJob, error) {
	j := &store.ProvisioningJob{}
	err := scanJob(s.db.QueryRowContext(ctx,
		`UPDATE provisioning_jobs SET status='in_progress'
		 WHERE id = (
		     SELECT id FROM provisioning_jobs
		     WHERE status='pending' AND scheduled_at <= now()
		     ORDER BY scheduled_at ASC
		     FOR UPDATE SKIP LOCKED
		     LIMIT 1
		 )
		 RETURNING `+jobCols), j)
	if err != nil {
		return nil, mapErr(err)
	}
	return j, nil
}

func (s *Store) UpdateJob(ctx context.Context, j *store.ProvisioningJob) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE provisioning_jobs SET status=$2, attempts=$3, last_error=$4, scheduled_at=$5 WHERE id=$1`,
		j.ID, string(j.Status), j.Attempts, j.LastError, j.ScheduledAt)
	return affected(res, err)
}

// affected maps a missing row to ErrNotFound for update statements.
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
