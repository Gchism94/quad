// SPDX-License-Identifier: AGPL-3.0-or-later

package importer

import (
	"strings"
	"testing"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter/github/classroom"
)

// fixture loads the synthetic GitHub Classroom snapshot in testdata. It mirrors
// the real export schema; see testdata/README.md.
func fixture(t *testing.T) *classroom.Snapshot {
	t.Helper()
	snap, err := classroom.ReadSnapshot("testdata/snapshot")
	if err != nil {
		t.Fatalf("read fixture snapshot: %v", err)
	}
	return snap
}

func planFromFixture(t *testing.T) (Plan, []string) {
	t.Helper()
	p, warnings, err := FromGitHubClassroom(fixture(t), "")
	if err != nil {
		t.Fatalf("FromGitHubClassroom: %v", err)
	}
	return p, warnings
}

func findAssignment(t *testing.T, p Plan, slug string) PlannedAssignment {
	t.Helper()
	for _, a := range p.Assignments {
		if a.Slug == slug {
			return a
		}
	}
	t.Fatalf("assignment %q not in plan", slug)
	return PlannedAssignment{}
}

func TestFromGitHubClassroomMapsCourse(t *testing.T) {
	p, _ := planFromFixture(t)

	if p.Host != adapter.HostGitHub {
		t.Errorf("host = %q, want %q", p.Host, adapter.HostGitHub)
	}
	if p.Namespace != "CS-101-F26" {
		t.Errorf("namespace = %q, want CS-101-F26", p.Namespace)
	}
	if p.Name != "CS-101-F26-classroom" {
		t.Errorf("name = %q", p.Name)
	}
	if p.Source != SourceGitHubClassroom {
		t.Errorf("source = %q, want %q", p.Source, SourceGitHubClassroom)
	}
	if len(p.Assignments) != 3 {
		t.Fatalf("got %d assignments, want 3", len(p.Assignments))
	}
}

func TestFromGitHubClassroomMapsIndividualAssignment(t *testing.T) {
	p, _ := planFromFixture(t)
	hw1 := findAssignment(t, p, "hw-01")

	if hw1.Title != "Python Foundations" {
		t.Errorf("title = %q", hw1.Title)
	}
	if hw1.Group {
		t.Error("hw-01 should not be a group assignment")
	}
	want := time.Date(2026, 2, 7, 1, 0, 0, 0, time.UTC)
	if hw1.Deadline == nil || !hw1.Deadline.Equal(want) {
		t.Errorf("deadline = %v, want %v", hw1.Deadline, want)
	}
	wantTmpl := adapter.TemplateRef{
		Host:      adapter.HostGitHub,
		Namespace: "CS-101-F26",
		Name:      "cs-101-f26-classroom-python-foundations-hw-01",
		Ref:       "main",
	}
	if hw1.Template != wantTmpl {
		t.Errorf("template = %+v, want %+v", hw1.Template, wantTmpl)
	}
	if len(hw1.Repos) != 3 {
		t.Fatalf("got %d repos, want 3", len(hw1.Repos))
	}

	// Repository names come from the host, never reconstructed from a convention.
	byName := map[string]PlannedRepo{}
	for _, r := range hw1.Repos {
		byName[r.Name] = r
	}
	graded, ok := byName["hw-01-student01"]
	if !ok {
		t.Fatal("hw-01-student01 missing")
	}
	if got := graded.Members; len(got) != 1 || got[0] != "student01" {
		t.Errorf("members = %v", got)
	}
	if graded.Score == nil || *graded.Score != 10 || graded.MaxScore == nil || *graded.MaxScore != 90 {
		t.Errorf("grade = %v/%v, want 10/90", graded.Score, graded.MaxScore)
	}
	if graded.GradeRaw != "10/90" {
		t.Errorf("raw grade = %q", graded.GradeRaw)
	}

	// A null grade must stay nil rather than becoming a zero score.
	ungraded := byName["hw-01-student02"]
	if ungraded.Score != nil || ungraded.MaxScore != nil {
		t.Errorf("ungraded repo got a score: %v/%v", ungraded.Score, ungraded.MaxScore)
	}
}

func TestFromGitHubClassroomMapsGroupAssignment(t *testing.T) {
	p, _ := planFromFixture(t)
	fp := findAssignment(t, p, "final-project")

	if !fp.Group {
		t.Error("final-project should be a group assignment")
	}
	if fp.Deadline != nil {
		t.Errorf("deadline = %v, want none", fp.Deadline)
	}
	var team PlannedRepo
	for _, r := range fp.Repos {
		if r.Name == "final-project-team-alpha" {
			team = r
		}
	}
	if len(team.Members) != 2 {
		t.Fatalf("team members = %v, want 2", team.Members)
	}
}

func TestFromGitHubClassroomWarnsInsteadOfGuessing(t *testing.T) {
	p, warnings := planFromFixture(t)

	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "n/a") {
		t.Errorf("expected a warning about the unparseable grade, got:\n%s", joined)
	}
	if !strings.Contains(joined, "hw-02-orphaned") {
		t.Errorf("expected a warning about the acceptance with no students, got:\n%s", joined)
	}

	hw2 := findAssignment(t, p, "hw-02")
	if len(hw2.Repos) != 2 {
		t.Fatalf("got %d repos for hw-02, want 2 (the one with no students is dropped)", len(hw2.Repos))
	}
	for _, r := range hw2.Repos {
		if r.Name == "hw-02-orphaned" {
			t.Error("a repo with no student accounts must not be imported")
		}
		if r.Name == "hw-02-student02" && (r.Score != nil || r.MaxScore != nil) {
			t.Error("an unparseable grade must not produce a score")
		}
	}
	// An assignment with no starter code still imports; it is simply not
	// re-generatable from a template.
	if hw2.Template != (adapter.TemplateRef{}) {
		t.Errorf("template = %+v, want zero", hw2.Template)
	}
}

func TestFromGitHubClassroomOrgOverrideAndMismatch(t *testing.T) {
	// A repo that does not live in the target org cannot be addressed by a plan
	// with a single namespace, so it must be reported and dropped — not silently
	// rewritten into the wrong org.
	p, warnings, err := FromGitHubClassroom(fixture(t), "SOME-OTHER-ORG")
	if err != nil {
		t.Fatalf("FromGitHubClassroom: %v", err)
	}
	if p.Namespace != "SOME-OTHER-ORG" {
		t.Errorf("namespace = %q, want the override", p.Namespace)
	}
	for _, a := range p.Assignments {
		if len(a.Repos) != 0 {
			t.Errorf("assignment %s kept %d repos from another org", a.Slug, len(a.Repos))
		}
	}
	if len(warnings) == 0 {
		t.Error("expected warnings about repos outside the org")
	}
}

func TestFromGitHubClassroomRejectsUnusableInput(t *testing.T) {
	if _, _, err := FromGitHubClassroom(nil, ""); err == nil {
		t.Error("nil snapshot should error")
	}
	snap := &classroom.Snapshot{Classroom: classroom.Classroom{Name: "no org"}}
	if _, _, err := FromGitHubClassroom(snap, ""); err == nil {
		t.Error("snapshot with no organization should error")
	}
}
