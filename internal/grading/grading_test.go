// SPDX-License-Identifier: AGPL-3.0-or-later

package grading

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/internal/store/memory"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
	"github.com/EduCloud-Ecosystem/cairn/pkg/gradingspec"
)

func TestExecRunnerScoresByMatchAndExit(t *testing.T) {
	dir := t.TempDir()
	spec := gradingspec.Spec{
		Version: "1",
		Tests: []gradingspec.Test{
			{Name: "greets", Run: "echo hello", Points: 5, Match: &gradingspec.OutputMatch{Expected: "hello", Trim: true}},
			{Name: "exit-ok", Run: "true", Points: 3},   // exit 0 -> pass
			{Name: "exit-bad", Run: "false", Points: 4}, // exit 1 -> fail
			{Name: "wrong-output", Run: "echo nope", Points: 2, Match: &gradingspec.OutputMatch{Expected: "yes", Trim: true}},
		},
	}
	res, err := NewExecRunner().Run(context.Background(), spec, dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.MaxScore != 14 {
		t.Fatalf("MaxScore = %v, want 14", res.MaxScore)
	}
	if res.Score != 8 { // 5 + 3
		t.Fatalf("Score = %v, want 8", res.Score)
	}
	want := map[string]bool{"greets": true, "exit-ok": true, "exit-bad": false, "wrong-output": false}
	for _, tr := range res.Tests {
		if tr.Passed != want[tr.Name] {
			t.Errorf("test %q passed=%v, want %v", tr.Name, tr.Passed, want[tr.Name])
		}
	}
}

func TestExecRunnerSetupFailureZeroesScore(t *testing.T) {
	spec := gradingspec.Spec{
		Setup: []string{"false"}, // failing setup
		Tests: []gradingspec.Test{{Name: "t", Run: "true", Points: 10}},
	}
	res, err := NewExecRunner().Run(context.Background(), spec, t.TempDir())
	if err != nil {
		t.Fatalf("Run should not error on setup failure: %v", err)
	}
	if res.Score != 0 || res.MaxScore != 10 {
		t.Fatalf("score=%v max=%v, want 0/10", res.Score, res.MaxScore)
	}
}

// fakeCheckout writes the provided files into the target dir.
type fakeCheckout struct{ files map[string]string }

func (f fakeCheckout) Fetch(_ context.Context, _ adapter.RepoRef, dir string) error {
	for name, content := range f.files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func seedSubmission(t *testing.T, st *memory.Store) {
	t.Helper()
	ctx := context.Background()
	_ = st.CreateAssignment(ctx, &store.Assignment{ID: "a1", ClassroomID: "c1", Slug: "hw1", GradingSpec: "grading.json"})
	_ = st.CreateSubmission(ctx, &store.Submission{
		ID: "s1", AssignmentID: "a1", Status: "active",
		Repo: adapter.RepoRef{Host: adapter.HostGitHub, Namespace: "org", Name: "hw1-bob"},
	})
}

func TestServiceGradeWritesGradeAndRun(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	seedSubmission(t, st)

	spec := gradingspec.Spec{Version: "1", Tests: []gradingspec.Test{
		{Name: "t1", Run: "echo hi", Points: 7, Match: &gradingspec.OutputMatch{Expected: "hi", Trim: true}},
		{Name: "t2", Run: "false", Points: 3},
	}}
	specJSON, _ := json.Marshal(spec)
	svc := NewService(st, NewExecRunner(), fakeCheckout{files: map[string]string{"grading.json": string(specJSON)}})

	if err := svc.Grade(ctx, "s1"); err != nil {
		t.Fatalf("Grade: %v", err)
	}

	g, err := st.LatestGradeForSubmission(ctx, "s1")
	if err != nil {
		t.Fatalf("no grade written: %v", err)
	}
	if g.Score != 7 || g.MaxScore != 10 {
		t.Fatalf("grade score=%v max=%v, want 7/10", g.Score, g.MaxScore)
	}
	if g.RunID == "" {
		t.Fatal("grade missing RunID")
	}

	runs, _ := st.ListGradingRunsBySubmission(ctx, "s1")
	if len(runs) != 1 || runs[0].Status != "completed" {
		t.Fatalf("grading runs = %+v, want one completed", runs)
	}
	if runs[0].StartedAt == nil || runs[0].FinishedAt == nil {
		t.Fatal("grading run missing timestamps")
	}
}

func TestServiceGradeUnprovisionedFails(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1", Status: "provisioning"})
	svc := NewService(st, NewExecRunner(), fakeCheckout{})
	if err := svc.Grade(ctx, "s1"); err == nil {
		t.Fatal("expected an error grading an unprovisioned submission")
	}
	if _, err := st.LatestGradeForSubmission(ctx, "s1"); err == nil {
		t.Fatal("no grade should have been written")
	}
}

func TestServiceGradeBadSpecMarksRunFailed(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	seedSubmission(t, st)
	svc := NewService(st, NewExecRunner(), fakeCheckout{files: map[string]string{"grading.json": "{ not valid json"}})

	if err := svc.Grade(ctx, "s1"); err == nil {
		t.Fatal("expected an error for a malformed spec")
	}
	runs, _ := st.ListGradingRunsBySubmission(ctx, "s1")
	if len(runs) != 1 || runs[0].Status != "failed" {
		t.Fatalf("grading runs = %+v, want one failed", runs)
	}
}

func TestLoadSpecRejectsEmptyRun(t *testing.T) {
	dir := t.TempDir()
	specJSON := `{"tests":[{"name":"t1","run":"","points":5}]}`
	path := filepath.Join(dir, "grading.json")
	if err := os.WriteFile(path, []byte(specJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadSpec(path)
	if err == nil {
		t.Fatal("expected error for spec with empty run field, got nil")
	}
	if !contains(err.Error(), "empty run") {
		t.Errorf("error = %q, want it to mention 'empty run'", err.Error())
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub)))
}

func containsStr(s, sub string) bool {
	for i := range s {
		if i+len(sub) <= len(s) && s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
