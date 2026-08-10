// SPDX-License-Identifier: AGPL-3.0-or-later

package provisioning

import (
	"context"
	"testing"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/internal/store/memory"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

// TestPurgeJobDisabledWhenRetentionUnset confirms RetentionDays<=0 (the unset
// CAIRN_GRADE_RETENTION_DAYS case) is a no-op rather than purging with a
// zero-day window — 0 must mean "disabled", never "purge everything".
func TestPurgeJobDisabledWhenRetentionUnset(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	now := time.Date(2025, 12, 2, 0, 0, 0, 0, time.UTC)
	old := now.Add(-1000 * 24 * time.Hour)

	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c1", Name: "CS101", Host: adapter.HostGitHub, HostNamespace: "org"})
	_ = st.CreateAssignment(ctx, &store.Assignment{ID: "a1", ClassroomID: "c1", Slug: "hw1"})
	_ = st.CreateRosterEntry(ctx, &store.RosterEntry{ID: "r1", ClassroomID: "c1", Host: adapter.HostGitHub, HostUsername: "octocat"})
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1", RosterEntryID: "r1"})
	_ = st.CreateGrade(ctx, &store.Grade{ID: "g1", SubmissionID: "s1", Score: 80, MaxScore: 100, GradedAt: old, ExportConfirmedAt: &old})

	job := &PurgeJob{Store: st, RetentionDays: 0}
	grades, runs, err := job.RunOnce(ctx, now)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if grades != 0 || runs != 0 {
		t.Fatalf("purged grades=%d runs=%d, want 0,0 (retention unset)", grades, runs)
	}
	if _, err := st.LatestGradeForSubmission(ctx, "s1"); err != nil {
		t.Fatalf("grade should still exist: %v", err)
	}
}

// TestPurgeJobRemovesOnlyGradesPastRetention confirms the job only removes
// confirmed-exported grades whose retention window has elapsed, leaving a
// recently-confirmed grade and a never-confirmed grade untouched regardless
// of age.
func TestPurgeJobRemovesOnlyGradesPastRetention(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	now := time.Date(2025, 12, 2, 0, 0, 0, 0, time.UTC)
	old := now.Add(-40 * 24 * time.Hour)   // past a 30-day window
	recent := now.Add(-5 * 24 * time.Hour) // inside a 30-day window

	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c1", Name: "CS101", Host: adapter.HostGitHub, HostNamespace: "org"})
	_ = st.CreateAssignment(ctx, &store.Assignment{ID: "a1", ClassroomID: "c1", Slug: "hw1"})
	_ = st.CreateRosterEntry(ctx, &store.RosterEntry{ID: "r1", ClassroomID: "c1", Host: adapter.HostGitHub, HostUsername: "octocat"})
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1", RosterEntryID: "r1"})
	_ = st.CreateGrade(ctx, &store.Grade{ID: "g_old", SubmissionID: "s1", Score: 80, MaxScore: 100, GradedAt: old, RunID: "gr_old", ExportConfirmedAt: &old})
	_ = st.CreateGradingRun(ctx, &store.GradingRun{ID: "gr_old", SubmissionID: "s1", Status: "succeeded"})
	_ = st.CreateGrade(ctx, &store.Grade{ID: "g_recent", SubmissionID: "s1", Score: 85, MaxScore: 100, GradedAt: recent, ExportConfirmedAt: &recent})
	_ = st.CreateGrade(ctx, &store.Grade{ID: "g_unconfirmed", SubmissionID: "s1", Score: 10, MaxScore: 100, GradedAt: old})

	job := &PurgeJob{Store: st, RetentionDays: 30}
	grades, runs, err := job.RunOnce(ctx, now)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if grades != 1 {
		t.Errorf("grades purged = %d, want 1 (only g_old)", grades)
	}
	if runs != 1 {
		t.Errorf("runs purged = %d, want 1 (only gr_old)", runs)
	}

	hist, err := st.ListGradesBySubmission(ctx, "s1")
	if err != nil {
		t.Fatalf("ListGradesBySubmission: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("remaining grades = %d, want 2 (g_recent, g_unconfirmed)", len(hist))
	}
	for _, g := range hist {
		if g.ID == "g_old" {
			t.Errorf("g_old should have been purged")
		}
	}
}
