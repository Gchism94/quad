// SPDX-License-Identifier: AGPL-3.0-or-later

package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/id"
	"github.com/EduCloud-Ecosystem/cairn/internal/provisioning"
	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

// importedSubmissionStatus is the state an imported submission lands in. The repo
// already exists on the host, so it is "active" — the same status the provisioning
// worker sets once it has finished creating one. It is deliberately NOT
// "provisioning", which would invite the worker to create a repo that is already
// there.
const importedSubmissionStatus = "active"

// Options controls one Apply run.
type Options struct {
	// DryRun prints the plan and writes nothing. Reads still happen, so a dry run
	// against a populated store correctly distinguishes "create" from "reuse".
	DryRun bool
	// CreatedBy is the operator User.ID recorded on a created classroom.
	CreatedBy string
	// ImportGrades records historical scores from the plan.
	ImportGrades bool
	// SkipGroup leaves team assignments out entirely.
	SkipGroup bool
	// RetroactiveLock allows an already-passed deadline to lock the imported
	// repositories. It is off by default: see suppressRetroactiveLocks.
	RetroactiveLock bool
	// Now overrides the clock, for deterministic tests.
	Now time.Time
	// Out receives the human-readable plan. nil discards it.
	Out io.Writer
}

// Result counts what Apply did — or, under DryRun, what it would do.
type Result struct {
	ClassroomID      string
	ClassroomCreated bool

	AssignmentsCreated int
	AssignmentsReused  int
	AssignmentsSkipped int // group assignments skipped via SkipGroup

	RosterCreated int
	RosterReused  int

	SubmissionsCreated int
	SubmissionsReused  int

	GradesCreated  int
	GradesReused   int
	GradesUngraded int // repos the host reported no grade for

	// TeamsCollapsed counts shared repositories attached to a single primary
	// member because store.Submission holds one roster entry.
	TeamsCollapsed int

	// PastDeadlines is the number of imported assignments whose deadline had
	// already passed; LocksSuppressed is the number of lock jobs pre-spent so the
	// scheduler does not retroactively lock those repositories. On a re-import
	// LocksSuppressed is 0 because the keys are already spent — which is why
	// RetroactiveLock records the option rather than being inferred from it.
	PastDeadlines   int
	LocksSuppressed int
	RetroactiveLock bool
}

// Summary renders a short report suitable for printing after an import.
func (r Result) Summary() string {
	var b strings.Builder
	verb := func(created, reused int) string {
		return fmt.Sprintf("%d created, %d already present", created, reused)
	}
	clsState := "existing"
	if r.ClassroomCreated {
		clsState = "created"
	}
	fmt.Fprintf(&b, "classroom:   %s (%s)\n", clsState, r.ClassroomID)
	fmt.Fprintf(&b, "assignments: %s", verb(r.AssignmentsCreated, r.AssignmentsReused))
	if r.AssignmentsSkipped > 0 {
		fmt.Fprintf(&b, ", %d group assignment(s) skipped", r.AssignmentsSkipped)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "roster:      %s\n", verb(r.RosterCreated, r.RosterReused))
	fmt.Fprintf(&b, "submissions: %s\n", verb(r.SubmissionsCreated, r.SubmissionsReused))
	fmt.Fprintf(&b, "grades:      %s, %d repo(s) had no grade to import\n", verb(r.GradesCreated, r.GradesReused), r.GradesUngraded)
	if r.TeamsCollapsed > 0 {
		fmt.Fprintf(&b, "teams:       %d shared repo(s) attached to one primary member (Cairn has no team model yet)\n", r.TeamsCollapsed)
	}
	if r.PastDeadlines > 0 {
		if r.RetroactiveLock {
			fmt.Fprintf(&b, "deadlines:   %d assignment(s) already past; --retroactive-lock is set, so the scheduler WILL lock their repos\n",
				r.PastDeadlines)
		} else {
			fmt.Fprintf(&b, "deadlines:   %d assignment(s) already past; %d lock job(s) pre-spent, so no repo is locked retroactively\n",
				r.PastDeadlines, r.LocksSuppressed)
		}
	}
	return b.String()
}

// Apply writes the plan into st. It is idempotent: rows are matched on their
// natural keys (classroom by host+namespace, assignment by slug, roster entry by
// username, submission by assignment+roster entry), so re-running after a partial
// failure resumes rather than duplicates.
//
// Apply never contacts a Git host.
func Apply(ctx context.Context, st store.Store, p Plan, opts Options) (*Result, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	a := &applier{
		st:   st,
		plan: p,
		opts: opts,
		res:  &Result{RetroactiveLock: opts.RetroactiveLock},
		out:  opts.Out,
		now:  opts.Now,
	}
	if a.out == nil {
		a.out = io.Discard
	}
	if a.now.IsZero() {
		a.now = time.Now().UTC()
	}
	if err := a.run(ctx); err != nil {
		return nil, err
	}
	return a.res, nil
}

func (p Plan) validate() error {
	switch {
	case p.Host == "":
		return errors.New("importer: plan has no host")
	case p.Namespace == "":
		return errors.New("importer: plan has no namespace (org/group slug)")
	case p.Name == "":
		return errors.New("importer: plan has no course name")
	}
	for _, as := range p.Assignments {
		if as.Slug == "" {
			return fmt.Errorf("importer: assignment %q has no slug", as.Title)
		}
		for _, r := range as.Repos {
			if r.Name == "" {
				return fmt.Errorf("importer: assignment %q has a repo with no name", as.Slug)
			}
			if len(r.Members) == 0 {
				return fmt.Errorf("importer: repo %q has no members", r.Name)
			}
		}
	}
	return nil
}

type applier struct {
	st   store.Store
	plan Plan
	opts Options
	res  *Result
	out  io.Writer
	now  time.Time

	classroom *store.Classroom
	// roster caches entries by host username so a student appearing in twelve
	// assignments is created once, including under DryRun where nothing is
	// persisted for a later lookup to find.
	roster map[string]*store.RosterEntry
}

func (a *applier) run(ctx context.Context) error {
	if err := a.ensureClassroom(ctx); err != nil {
		return err
	}
	if err := a.loadRoster(ctx); err != nil {
		return err
	}
	existing, err := a.existingAssignmentsBySlug(ctx)
	if err != nil {
		return err
	}
	for _, pa := range a.plan.Assignments {
		if pa.Group && a.opts.SkipGroup {
			a.res.AssignmentsSkipped++
			a.printf("assignment  skip     %-24s group assignment (--skip-group)\n", pa.Slug)
			continue
		}
		if err := a.applyAssignment(ctx, pa, existing); err != nil {
			return fmt.Errorf("assignment %s: %w", pa.Slug, err)
		}
	}
	return nil
}

func (a *applier) ensureClassroom(ctx context.Context) error {
	all, err := a.st.ListClassrooms(ctx)
	if err != nil {
		return fmt.Errorf("list classrooms: %w", err)
	}
	for _, c := range all {
		if c.Host == a.plan.Host && c.HostNamespace == a.plan.Namespace {
			a.classroom = c
			a.res.ClassroomID = c.ID
			a.printf("classroom   existing %s [%s:%s]\n", c.Name, c.Host, c.HostNamespace)
			return nil
		}
	}
	policy := a.plan.JoinPolicy
	if policy == "" {
		// The whole roster is known at import time, so restricting self-enrollment
		// to it is both safe and the more protective default.
		policy = store.ClassroomJoinPolicyRoster
	}
	c := &store.Classroom{
		ID:            id.New(),
		Name:          a.plan.Name,
		Host:          a.plan.Host,
		HostNamespace: a.plan.Namespace,
		JoinPolicy:    policy,
		CreatedBy:     a.opts.CreatedBy,
		CreatedAt:     a.now,
	}
	if !a.opts.DryRun {
		if err := a.st.CreateClassroom(ctx, c); err != nil {
			return fmt.Errorf("create classroom: %w", err)
		}
	}
	a.classroom = c
	a.res.ClassroomID = c.ID
	a.res.ClassroomCreated = true
	a.printf("classroom   create   %s [%s:%s] join-policy=%s\n", c.Name, c.Host, c.HostNamespace, policy)
	return nil
}

func (a *applier) loadRoster(ctx context.Context) error {
	a.roster = map[string]*store.RosterEntry{}
	if a.res.ClassroomCreated {
		return nil // nothing to load; the classroom is new (or, under DryRun, unsaved)
	}
	entries, err := a.st.ListRosterEntries(ctx, a.classroom.ID)
	if err != nil {
		return fmt.Errorf("list roster: %w", err)
	}
	for _, e := range entries {
		a.roster[e.HostUsername] = e
	}
	return nil
}

func (a *applier) existingAssignmentsBySlug(ctx context.Context) (map[string]*store.Assignment, error) {
	out := map[string]*store.Assignment{}
	if a.res.ClassroomCreated {
		return out, nil
	}
	list, err := a.st.ListAssignmentsByClassroom(ctx, a.classroom.ID)
	if err != nil {
		return nil, fmt.Errorf("list assignments: %w", err)
	}
	for _, as := range list {
		out[as.Slug] = as
	}
	return out, nil
}

func (a *applier) applyAssignment(ctx context.Context, pa PlannedAssignment, existing map[string]*store.Assignment) error {
	as, created := existing[pa.Slug], false
	if as == nil {
		kind := store.AssignmentIndividual
		if pa.Group {
			kind = store.AssignmentGroup
		}
		as = &store.Assignment{
			ID:          id.New(),
			ClassroomID: a.classroom.ID,
			Title:       pa.Title,
			Slug:        pa.Slug,
			TemplateRef: pa.Template,
			Type:        kind,
			Deadline:    pa.Deadline,
			CreatedAt:   a.now,
		}
		if !a.opts.DryRun {
			if err := a.st.CreateAssignment(ctx, as); err != nil {
				return fmt.Errorf("create assignment: %w", err)
			}
		}
		existing[pa.Slug] = as
		created = true
		a.res.AssignmentsCreated++
	} else {
		a.res.AssignmentsReused++
	}

	past := pa.Deadline != nil && pa.Deadline.Before(a.now)
	if past {
		a.res.PastDeadlines++
	}
	a.printf("assignment  %-8s %-22s %-40q %s%s\n",
		state(created), pa.Slug, pa.Title, deadlineLabel(pa.Deadline, past, a.opts.RetroactiveLock), typeLabel(pa))

	for _, pr := range pa.Repos {
		if err := a.applyRepo(ctx, as, pr, past); err != nil {
			return fmt.Errorf("repo %s: %w", pr.Name, err)
		}
	}
	return nil
}

func (a *applier) applyRepo(ctx context.Context, as *store.Assignment, pr PlannedRepo, pastDeadline bool) error {
	members := append([]string(nil), pr.Members...)
	sort.Strings(members)
	if len(members) > 1 {
		a.res.TeamsCollapsed++
	}

	// Every member gets a roster entry — the team is recorded even though only the
	// primary member can own the submission row.
	var primary *store.RosterEntry
	for i, username := range members {
		entry, err := a.ensureRosterEntry(ctx, username)
		if err != nil {
			return err
		}
		if i == 0 {
			primary = entry
		}
	}

	sub, subCreated, err := a.ensureSubmission(ctx, as, primary, pr.Name)
	if err != nil {
		return err
	}

	gradeState := "-"
	switch {
	case !a.opts.ImportGrades:
		gradeState = "skipped"
	case pr.Score == nil || pr.MaxScore == nil:
		a.res.GradesUngraded++
		gradeState = "none"
	default:
		created, err := a.ensureGrade(ctx, sub, pr)
		if err != nil {
			return err
		}
		gradeState = fmt.Sprintf("%s %s", state(created), pr.GradeRaw)
	}

	a.printf("  repo      %-8s %-44s grade:%s%s\n", state(subCreated), pr.Name, gradeState, teamLabel(members))

	if pastDeadline && !a.opts.RetroactiveLock {
		if err := a.suppressRetroactiveLock(ctx, sub); err != nil {
			return err
		}
	}
	return nil
}

func (a *applier) ensureRosterEntry(ctx context.Context, username string) (*store.RosterEntry, error) {
	if e, ok := a.roster[username]; ok {
		a.res.RosterReused++
		return e, nil
	}
	e := &store.RosterEntry{
		ID:          id.New(),
		ClassroomID: a.classroom.ID,
		Host:        a.plan.Host,
		// The host username is the only student identifier imported. Nothing else
		// about the student crosses this boundary.
		HostUsername: username,
		// These students already accepted the assignment and have a repo, so they
		// are active rather than invited.
		Status: store.RosterActive,
	}
	if !a.opts.DryRun {
		if err := a.st.CreateRosterEntry(ctx, e); err != nil {
			return nil, fmt.Errorf("create roster entry %s: %w", username, err)
		}
	}
	a.roster[username] = e
	a.res.RosterCreated++
	return e, nil
}

func (a *applier) ensureSubmission(ctx context.Context, as *store.Assignment, entry *store.RosterEntry, repoName string) (*store.Submission, bool, error) {
	found, err := a.st.FindSubmission(ctx, as.ID, entry.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, false, fmt.Errorf("find submission: %w", err)
	}
	if found != nil {
		a.res.SubmissionsReused++
		return found, false, nil
	}
	sub := &store.Submission{
		ID:            id.New(),
		AssignmentID:  as.ID,
		RosterEntryID: entry.ID,
		Repo: adapter.RepoRef{
			Host:      a.plan.Host,
			Namespace: a.plan.Namespace,
			Name:      repoName,
		},
		Status: importedSubmissionStatus,
	}
	if !a.opts.DryRun {
		if err := a.st.CreateSubmission(ctx, sub); err != nil {
			return nil, false, fmt.Errorf("create submission: %w", err)
		}
	}
	a.res.SubmissionsCreated++
	return sub, true, nil
}

// gradeProvenance is stored as an imported grade's breakdown so a score that came
// from another platform is never mistaken for one Cairn produced.
type gradeProvenance struct {
	Source string `json:"source"`
	Raw    string `json:"raw"`
}

func (a *applier) ensureGrade(ctx context.Context, sub *store.Submission, pr PlannedRepo) (bool, error) {
	prov := gradeProvenance{Source: a.plan.Source, Raw: pr.GradeRaw}
	breakdown, err := json.Marshal(prov)
	if err != nil {
		return false, err
	}

	existing, err := a.st.ListGradesBySubmission(ctx, sub.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, fmt.Errorf("list grades: %w", err)
	}
	for _, g := range existing {
		if g.Score != *pr.Score || g.MaxScore != *pr.MaxScore {
			continue
		}
		var got gradeProvenance
		if json.Unmarshal(g.Breakdown, &got) == nil && got == prov {
			a.res.GradesReused++
			return false, nil
		}
	}

	g := &store.Grade{
		ID:           id.New(),
		SubmissionID: sub.ID,
		Score:        *pr.Score,
		MaxScore:     *pr.MaxScore,
		Breakdown:    breakdown,
		// The source platform exposes no trustworthy grading timestamp, so this is
		// the import time and the breakdown says where the score came from. See
		// docs/ghc-import.md.
		GradedAt: a.now,
	}
	if !a.opts.DryRun {
		if err := a.st.CreateGrade(ctx, g); err != nil {
			return false, fmt.Errorf("create grade: %w", err)
		}
	}
	a.res.GradesCreated++
	return true, nil
}

// suppressRetroactiveLock stops an imported past deadline from locking a
// repository that has been sitting finished for months.
//
// The scheduler enqueues a lock job for every submission of every assignment past
// its deadline, keyed "lock:<submissionID>". Importing a finished semester would
// therefore lock every imported repo — a mass write to a Git host, triggered by an
// import that otherwise never touches one. Pre-spending that key makes the
// scheduler's later enqueue a no-op. This is the same mechanism the scheduler
// already relies on so that an instructor's manual unlock for an extension is not
// fought on the next tick.
func (a *applier) suppressRetroactiveLock(ctx context.Context, sub *store.Submission) error {
	if a.opts.DryRun {
		a.res.LocksSuppressed++
		return nil
	}
	created, err := a.st.CreateJob(ctx, &store.ProvisioningJob{
		ID:             id.New(),
		Type:           string(provisioning.JobLockRepo),
		TargetRef:      sub.ID,
		Status:         store.JobSucceeded,
		IdempotencyKey: "lock:" + sub.ID,
		ScheduledAt:    a.now,
	})
	if err != nil {
		return fmt.Errorf("pre-spend lock key for submission %s: %w", sub.ID, err)
	}
	if created {
		a.res.LocksSuppressed++
	}
	return nil
}

func (a *applier) printf(format string, args ...any) {
	fmt.Fprintf(a.out, format, args...)
}

func state(created bool) string {
	if created {
		return "create"
	}
	return "existing"
}

func typeLabel(pa PlannedAssignment) string {
	if pa.Group {
		return "  [group]"
	}
	return ""
}

func teamLabel(members []string) string {
	if len(members) < 2 {
		return ""
	}
	return fmt.Sprintf("  team=[%s] primary=%s", strings.Join(members, " "), members[0])
}

func deadlineLabel(deadline *time.Time, past, retroactiveLock bool) string {
	if deadline == nil {
		return "deadline=none"
	}
	s := "deadline=" + deadline.UTC().Format(time.RFC3339)
	if !past {
		return s
	}
	if retroactiveLock {
		return s + " (past; repos WILL be locked)"
	}
	return s + " (past; locks suppressed)"
}
