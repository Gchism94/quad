// SPDX-License-Identifier: AGPL-3.0-or-later

package importer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/provisioning"
	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/internal/store/memory"
)

// now is a fixed clock: after the fixture's hw-01 deadline (Feb 2026) and before
// hw-02's (2099), so one assignment is past due and the other is not.
var now = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

func defaultOptions() Options {
	return Options{ImportGrades: true, Now: now, Out: io.Discard}
}

func applyFixture(t *testing.T, st store.Store, opts Options) *Result {
	t.Helper()
	p, _, err := FromGitHubClassroom(fixture(t), "")
	if err != nil {
		t.Fatalf("FromGitHubClassroom: %v", err)
	}
	res, err := Apply(context.Background(), st, p, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return res
}

func TestApplyImportsWholeCourse(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	res := applyFixture(t, st, defaultOptions())

	if !res.ClassroomCreated {
		t.Error("classroom should have been created")
	}
	if res.AssignmentsCreated != 3 {
		t.Errorf("assignments created = %d, want 3", res.AssignmentsCreated)
	}
	// student01..03 appear across several assignments but are one roster entry each.
	if res.RosterCreated != 3 {
		t.Errorf("roster created = %d, want 3", res.RosterCreated)
	}
	// 3 (hw-01) + 2 (final-project) + 2 (hw-02, minus the studentless one).
	if res.SubmissionsCreated != 7 {
		t.Errorf("submissions created = %d, want 7", res.SubmissionsCreated)
	}
	if res.GradesCreated != 3 {
		t.Errorf("grades created = %d, want 3", res.GradesCreated)
	}
	if res.GradesUngraded != 4 {
		t.Errorf("ungraded repos = %d, want 4", res.GradesUngraded)
	}

	cls, err := st.ListClassrooms(ctx)
	if err != nil || len(cls) != 1 {
		t.Fatalf("ListClassrooms = %v, %v", cls, err)
	}
	if cls[0].JoinPolicy != store.ClassroomJoinPolicyRoster {
		t.Errorf("join policy = %q, want roster by default", cls[0].JoinPolicy)
	}

	roster, err := st.ListRosterEntries(ctx, cls[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range roster {
		if e.Status != store.RosterActive {
			t.Errorf("%s status = %q, want active", e.HostUsername, e.Status)
		}
		// The privacy invariant: a host username and nothing else.
		if e.EmailHash != "" {
			t.Errorf("%s carries an email hash; the importer must not set one", e.HostUsername)
		}
	}
}

// Imported submissions must be "active" — the repos already exist. "provisioning"
// would invite the worker to create repos that are already there.
func TestApplyMarksSubmissionsActiveAndPointsAtExistingRepos(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	applyFixture(t, st, defaultOptions())

	cls, _ := st.ListClassrooms(ctx)
	subs, err := st.ListSubmissionsByClassroom(ctx, cls[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 7 {
		t.Fatalf("got %d submissions, want 7", len(subs))
	}
	for _, s := range subs {
		if s.Status != importedSubmissionStatus {
			t.Errorf("submission %s status = %q, want %q", s.Repo.Name, s.Status, importedSubmissionStatus)
		}
		if s.Repo.Namespace != "CS-101-F26" {
			t.Errorf("submission %s namespace = %q", s.Repo.Name, s.Repo.Namespace)
		}
	}

	// The webhook receiver routes pushes by repo, so the mapping must resolve.
	sub, err := st.FindSubmissionByRepo(ctx, "github", "CS-101-F26", "hw-01-student01")
	if err != nil {
		t.Fatalf("FindSubmissionByRepo: %v", err)
	}
	if sub.Status != importedSubmissionStatus {
		t.Errorf("status = %q", sub.Status)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	st := memory.New()
	applyFixture(t, st, defaultOptions())
	second := applyFixture(t, st, defaultOptions())

	if second.ClassroomCreated {
		t.Error("second run recreated the classroom")
	}
	switch {
	case second.AssignmentsCreated != 0:
		t.Errorf("second run created %d assignments", second.AssignmentsCreated)
	case second.RosterCreated != 0:
		t.Errorf("second run created %d roster entries", second.RosterCreated)
	case second.SubmissionsCreated != 0:
		t.Errorf("second run created %d submissions", second.SubmissionsCreated)
	case second.GradesCreated != 0:
		t.Errorf("second run created %d grades", second.GradesCreated)
	}
	if second.AssignmentsReused != 3 || second.SubmissionsReused != 7 || second.GradesReused != 3 {
		t.Errorf("second run reuse counts = %d assignments, %d submissions, %d grades",
			second.AssignmentsReused, second.SubmissionsReused, second.GradesReused)
	}
}

func TestApplyDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	var out strings.Builder

	opts := defaultOptions()
	opts.DryRun = true
	opts.Out = &out
	res := applyFixture(t, st, opts)

	if res.SubmissionsCreated != 7 || res.RosterCreated != 3 {
		t.Errorf("dry run should still report the full plan, got %+v", res)
	}
	cls, err := st.ListClassrooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cls) != 0 {
		t.Fatalf("dry run wrote %d classrooms", len(cls))
	}
	plan := out.String()
	for _, want := range []string{"hw-01-student01", "final-project-team-alpha", "locks suppressed"} {
		if !strings.Contains(plan, want) {
			t.Errorf("printed plan missing %q:\n%s", want, plan)
		}
	}
}

// The core hazard: importing a finished semester must not make the scheduler lock
// every repo it just imported.
func TestApplySuppressesRetroactiveLocks(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	res := applyFixture(t, st, defaultOptions())

	// hw-01 is past due (3 repos); final-project has no deadline; hw-02 is future.
	if res.PastDeadlines != 1 {
		t.Errorf("past deadlines = %d, want 1", res.PastDeadlines)
	}
	if res.LocksSuppressed != 3 {
		t.Errorf("locks suppressed = %d, want 3", res.LocksSuppressed)
	}

	// Drive the real queue: suppression works by spending the idempotency key in
	// the store, so a fake queue would not exercise it. The scheduler still tries
	// to enqueue; what matters is that nothing runnable comes out the other end.
	sched := &provisioning.Scheduler{Store: st, Queue: provisioning.NewService(st)}
	if _, err := sched.RunOnce(ctx, now); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if job, err := st.ClaimNextJob(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a lock job became runnable (%+v, err %v); imported repos must not be locked retroactively", job, err)
	}
}

// On a re-import every lock key is already spent, so LocksSuppressed is 0. The
// summary must still report suppression rather than claiming --retroactive-lock
// was set.
func TestSummaryDoesNotInferRetroactiveLockFromCounters(t *testing.T) {
	st := memory.New()
	applyFixture(t, st, defaultOptions())
	second := applyFixture(t, st, defaultOptions())

	if second.LocksSuppressed != 0 {
		t.Fatalf("expected already-spent keys on re-import, got %d", second.LocksSuppressed)
	}
	if got := second.Summary(); strings.Contains(got, "WILL lock") {
		t.Errorf("re-import summary claims repos will be locked:\n%s", got)
	}

	opts := defaultOptions()
	opts.RetroactiveLock = true
	optedIn := applyFixture(t, memory.New(), opts)
	if got := optedIn.Summary(); !strings.Contains(got, "WILL lock") {
		t.Errorf("opted-in summary should warn that repos will be locked:\n%s", got)
	}
}

func TestApplyRetroactiveLockOptIn(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	opts := defaultOptions()
	opts.RetroactiveLock = true
	res := applyFixture(t, st, opts)

	if res.LocksSuppressed != 0 {
		t.Errorf("locks suppressed = %d, want 0 when opted in", res.LocksSuppressed)
	}
	sched := &provisioning.Scheduler{Store: st, Queue: provisioning.NewService(st)}
	n, err := sched.RunOnce(ctx, now)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if n != 3 {
		t.Errorf("scheduler enqueued %d lock job(s), want 3 for the past-due assignment", n)
	}
	job, err := st.ClaimNextJob(ctx)
	if err != nil {
		t.Fatalf("expected a runnable lock job when opted in: %v", err)
	}
	if job.Type != string(provisioning.JobLockRepo) {
		t.Errorf("job type = %q, want %q", job.Type, provisioning.JobLockRepo)
	}
}

func TestApplyGradeProvenanceAndOptOut(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	applyFixture(t, st, defaultOptions())

	sub, err := st.FindSubmissionByRepo(ctx, "github", "CS-101-F26", "hw-01-student01")
	if err != nil {
		t.Fatal(err)
	}
	g, err := st.LatestGradeForSubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("LatestGradeForSubmission: %v", err)
	}
	if g.Score != 10 || g.MaxScore != 90 {
		t.Errorf("grade = %v/%v, want 10/90", g.Score, g.MaxScore)
	}
	var prov gradeProvenance
	if err := json.Unmarshal(g.Breakdown, &prov); err != nil {
		t.Fatalf("breakdown is not provenance JSON: %v", err)
	}
	if prov.Source != SourceGitHubClassroom || prov.Raw != "10/90" {
		t.Errorf("provenance = %+v", prov)
	}
	if !g.GradedAt.Equal(now) {
		t.Errorf("graded_at = %v, want the import time %v", g.GradedAt, now)
	}

	// --no-grades leaves scores out entirely.
	st2 := memory.New()
	opts := defaultOptions()
	opts.ImportGrades = false
	res := applyFixture(t, st2, opts)
	if res.GradesCreated != 0 {
		t.Errorf("grades created = %d with ImportGrades=false", res.GradesCreated)
	}
}

func TestApplyGroupAssignments(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	res := applyFixture(t, st, defaultOptions())

	// One two-member team collapses onto a primary member; the solo team does not.
	if res.TeamsCollapsed != 1 {
		t.Errorf("teams collapsed = %d, want 1", res.TeamsCollapsed)
	}
	sub, err := st.FindSubmissionByRepo(ctx, "github", "CS-101-F26", "final-project-team-alpha")
	if err != nil {
		t.Fatalf("team repo not imported: %v", err)
	}
	entry, err := st.GetRosterEntry(ctx, sub.RosterEntryID)
	if err != nil {
		t.Fatal(err)
	}
	// Primary member is deterministic: first username in sorted order.
	if entry.HostUsername != "student01" {
		t.Errorf("primary member = %q, want student01", entry.HostUsername)
	}
	// Both teammates are still on the roster.
	cls, _ := st.ListClassrooms(ctx)
	if _, err := st.FindRosterEntryByUsername(ctx, cls[0].ID, "student02"); err != nil {
		t.Errorf("teammate student02 missing from roster: %v", err)
	}
}

func TestApplySkipGroup(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	opts := defaultOptions()
	opts.SkipGroup = true
	res := applyFixture(t, st, opts)

	if res.AssignmentsSkipped != 1 {
		t.Errorf("skipped = %d, want 1", res.AssignmentsSkipped)
	}
	if res.AssignmentsCreated != 2 {
		t.Errorf("created = %d, want 2", res.AssignmentsCreated)
	}
	if _, err := st.FindSubmissionByRepo(ctx, "github", "CS-101-F26", "final-project-team-alpha"); err == nil {
		t.Error("group repo was imported despite SkipGroup")
	}
}

func TestApplyReusesAnExistingClassroom(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	existing := &store.Classroom{
		ID: "pre-existing", Name: "Already Here", Host: "github", HostNamespace: "CS-101-F26",
		JoinPolicy: store.ClassroomJoinPolicyOpen,
	}
	if err := st.CreateClassroom(ctx, existing); err != nil {
		t.Fatal(err)
	}

	res := applyFixture(t, st, defaultOptions())
	if res.ClassroomCreated {
		t.Error("a classroom already bound to that org must be reused, not duplicated")
	}
	if res.ClassroomID != "pre-existing" {
		t.Errorf("classroom id = %q", res.ClassroomID)
	}
	all, _ := st.ListClassrooms(ctx)
	if len(all) != 1 {
		t.Errorf("got %d classrooms, want 1", len(all))
	}
}

func TestApplyRejectsIncompletePlans(t *testing.T) {
	ctx := context.Background()
	score := 1.0
	cases := map[string]Plan{
		"no host":      {Namespace: "org", Name: "n"},
		"no namespace": {Host: "github", Name: "n"},
		"no name":      {Host: "github", Namespace: "org"},
		"no slug": {Host: "github", Namespace: "org", Name: "n",
			Assignments: []PlannedAssignment{{Title: "t"}}},
		"repo with no members": {Host: "github", Namespace: "org", Name: "n",
			Assignments: []PlannedAssignment{{Slug: "s", Repos: []PlannedRepo{{Name: "r", Score: &score}}}}},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Apply(ctx, memory.New(), p, defaultOptions()); err == nil {
				t.Error("expected an error")
			}
		})
	}
}
