// SPDX-License-Identifier: AGPL-3.0-or-later

package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/internal/store/sqlite"
	"github.com/EduCloud-Ecosystem/cairn/internal/store/storetest"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

func open(t *testing.T) store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSQLiteConformance(t *testing.T) {
	storetest.Run(t, open)
}

// TestSQLiteDataSurvivesReopen verifies that data written before Close is
// visible after reopening the same file.
func TestSQLiteDataSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persist.db")
	ctx := context.Background()

	// Write.
	st1, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	if err := st1.CreateClassroom(ctx, &store.Classroom{
		ID:            "c1",
		Name:          "CS101",
		Host:          adapter.HostGitHub,
		HostNamespace: "cs101-org",
	}); err != nil {
		t.Fatalf("CreateClassroom: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen and read.
	st2, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	defer func() { _ = st2.Close() }()

	got, err := st2.GetClassroom(ctx, "c1")
	if err != nil {
		t.Fatalf("GetClassroom after reopen: %v", err)
	}
	if got.Name != "CS101" {
		t.Errorf("Name = %q, want CS101", got.Name)
	}
}

// TestSQLiteUniqueViolationMapsToErrConflict verifies that a duplicate
// (assignment_id, roster_entry_id) insert returns store.ErrConflict.
func TestSQLiteUniqueViolationMapsToErrConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conflict.db")
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c1", Name: "CS101", Host: adapter.HostGitHub, HostNamespace: "org"})
	_ = st.CreateAssignment(ctx, &store.Assignment{
		ID: "a1", ClassroomID: "c1", Title: "HW1", Slug: "hw-1",
		TemplateRef: adapter.TemplateRef{Host: adapter.HostGitHub, Namespace: "org", Name: "tpl"},
		Type:        store.AssignmentIndividual, GradingSpec: "grading.json",
	})
	_ = st.CreateRosterEntry(ctx, &store.RosterEntry{ID: "r1", ClassroomID: "c1", Host: adapter.HostGitHub, HostUsername: "octocat", Status: store.RosterInvited})

	if err := st.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1", RosterEntryID: "r1", Status: "provisioning"}); err != nil {
		t.Fatalf("first CreateSubmission: %v", err)
	}
	err = st.CreateSubmission(ctx, &store.Submission{ID: "s2", AssignmentID: "a1", RosterEntryID: "r1", Status: "provisioning"})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate CreateSubmission: got %v, want ErrConflict", err)
	}
}
