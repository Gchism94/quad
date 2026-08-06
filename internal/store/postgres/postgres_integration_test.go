// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build postgres

// Integration tests for the PostgreSQL store. They are compiled only with
// -tags postgres and skip unless CAIRN_TEST_DATABASE_URL points at a database
// you don't mind being TRUNCATEd. The tag gates these tests only — the shipped
// binary always includes the Postgres driver and selects a store at runtime.
//
// Run locally against deploy/docker-compose.dev.yml, which publishes 5433:
//
//	CAIRN_TEST_DATABASE_URL=postgres://cairn:cairn@localhost:5433/cairn?sslmode=disable \
//	  go test -tags postgres ./internal/store/postgres
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("CAIRN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set CAIRN_TEST_DATABASE_URL to run Postgres integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	s := New(db)
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`TRUNCATE grading_runs, grades, submissions, roster_entries, assignments, provisioning_jobs, classrooms, users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

// TestMigrateAppliesAllMigrations is the live-database half of the CC-P5
// regression guard: Migrate used to apply only 0001_init.up.sql, silently
// skipping 0002-0004. TestMigrationFilesAreOrderedAndComplete (no database)
// pins the file list; this asserts the schema those files produce actually
// exists after Migrate runs.
func TestMigrateAppliesAllMigrations(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	db := s.db

	var col, dtype, isNullable, colDefault string
	row := db.QueryRowContext(ctx, `
		SELECT column_name, data_type, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_name = 'submissions' AND column_name = 'last_error'`)
	if err := row.Scan(&col, &dtype, &isNullable, &colDefault); err != nil {
		t.Fatalf("submissions.last_error (0002): %v", err)
	}
	if dtype != "text" || isNullable != "NO" || colDefault != "''::text" {
		t.Fatalf("submissions.last_error = type=%s nullable=%s default=%s, want text/NO/''::text",
			dtype, isNullable, colDefault)
	}

	row = db.QueryRowContext(ctx, `
		SELECT column_name, data_type, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_name = 'classrooms' AND column_name = 'join_policy'`)
	if err := row.Scan(&col, &dtype, &isNullable, &colDefault); err != nil {
		t.Fatalf("classrooms.join_policy (0003): %v", err)
	}
	if dtype != "text" || isNullable != "NO" || colDefault != "'open'::text" {
		t.Fatalf("classrooms.join_policy = type=%s nullable=%s default=%s, want text/NO/'open'::text",
			dtype, isNullable, colDefault)
	}

	for _, idx := range []string{"idx_roster_host_username", "idx_submissions_repo"} {
		var name string
		if err := db.QueryRowContext(ctx,
			`SELECT indexname FROM pg_indexes WHERE indexname = $1`, idx,
		).Scan(&name); err != nil {
			t.Fatalf("index %s (0004): %v", idx, err)
		}
	}
}

func TestPostgresRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.CreateUser(ctx, &store.User{ID: "u1", Host: adapter.HostGitHub, HostUserID: "1", HostUsername: "prof", Email: "prof@example.edu"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateClassroom(ctx, &store.Classroom{ID: "c1", Name: "CS101", Host: adapter.HostGitHub, HostNamespace: "cs101", CreatedBy: "u1"}); err != nil {
		t.Fatal(err)
	}
	if cs, err := s.ListClassrooms(ctx); err != nil || len(cs) != 1 {
		t.Fatalf("ListClassrooms = %v, %v", cs, err)
	}

	due := time.Now().Add(-time.Hour).UTC()
	if err := s.CreateAssignment(ctx, &store.Assignment{
		ID: "a1", ClassroomID: "c1", Title: "HW1", Slug: "hw1",
		TemplateRef: adapter.TemplateRef{Host: adapter.HostGitHub, Namespace: "cs101", Name: "hw1-template"},
		Type:        store.AssignmentIndividual, Deadline: &due, GradingSpec: "grading.json",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAssignment(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Deadline == nil || !got.Deadline.Equal(due) {
		t.Fatalf("deadline round-trip: got %v want %v", got.Deadline, due)
	}
	if dueList, err := s.ListAssignmentsDueBy(ctx, time.Now()); err != nil || len(dueList) != 1 {
		t.Fatalf("ListAssignmentsDueBy = %v, %v", dueList, err)
	}

	if err := s.CreateRosterEntry(ctx, &store.RosterEntry{ID: "r1", ClassroomID: "c1", Host: adapter.HostGitHub, HostUsername: "alice", Status: store.RosterActive}); err != nil {
		t.Fatal(err)
	}
	if re, err := s.FindRosterEntryByUsername(ctx, "c1", "alice"); err != nil || re.ID != "r1" {
		t.Fatalf("FindRosterEntryByUsername = %v, %v", re, err)
	}
	if _, err := s.FindRosterEntryByUsername(ctx, "c1", "ghost"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing username: want ErrNotFound, got %v", err)
	}

	if err := s.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1", RosterEntryID: "r1", Repo: adapter.RepoRef{Host: adapter.HostGitHub, Namespace: "cs101", Name: "hw1-alice"}, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateGrade(ctx, &store.Grade{ID: "g1", SubmissionID: "s1", Score: 8, MaxScore: 10, Breakdown: []byte(`[{"name":"t1","passed":true}]`), RunID: "run1"}); err != nil {
		t.Fatal(err)
	}
	g, err := s.LatestGradeForSubmission(ctx, "s1")
	if err != nil || g.Score != 8 || g.MaxScore != 10 || len(g.Breakdown) == 0 {
		t.Fatalf("grade = %+v, %v", g, err)
	}

	start := time.Now().UTC()
	run := &store.GradingRun{ID: "run1", SubmissionID: "s1", Status: "running", Runner: "local-exec", StartedAt: &start}
	if err := s.CreateGradingRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	fin := time.Now().UTC()
	run.Status, run.FinishedAt, run.Result = "completed", &fin, []byte(`{"score":8}`)
	if err := s.UpdateGradingRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if runs, err := s.ListGradingRunsBySubmission(ctx, "s1"); err != nil || len(runs) != 1 || runs[0].Status != "completed" {
		t.Fatalf("runs = %v, %v", runs, err)
	}

	// Job idempotency + atomic claim.
	created, err := s.CreateJob(ctx, &store.ProvisioningJob{ID: "j1", Type: "create_repo", TargetRef: "s1", IdempotencyKey: "k1"})
	if err != nil || !created {
		t.Fatalf("first CreateJob created=%v err=%v", created, err)
	}
	dup, err := s.CreateJob(ctx, &store.ProvisioningJob{ID: "j2", Type: "create_repo", TargetRef: "s1", IdempotencyKey: "k1"})
	if err != nil || dup {
		t.Fatalf("duplicate CreateJob created=%v err=%v (want false)", dup, err)
	}
	claimed, err := s.ClaimNextJob(ctx)
	if err != nil || claimed.IdempotencyKey != "k1" || claimed.Status != store.JobInProgress {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if _, err := s.ClaimNextJob(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second claim: want ErrNotFound, got %v", err)
	}

	if err := s.UpdateSubmission(ctx, &store.Submission{ID: "missing"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update missing: want ErrNotFound, got %v", err)
	}
}
