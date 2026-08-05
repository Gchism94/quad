// SPDX-License-Identifier: AGPL-3.0-or-later

// Package grading executes an assignment's grading spec against a student's
// submission and records the score.
//
// SECURITY BOUNDARY: grading runs code that originates from the assignment's
// grading spec and from the student's repository. That is untrusted input. The
// Runner interface exists precisely so the execution backend is swappable: the
// only runner shipped here, ExecRunner, runs commands directly on the host with
// no isolation beyond a wall-clock timeout and is therefore UNSAFE for untrusted
// student code. A production deployment must provide a Runner that enforces real
// isolation (container/microVM with seccomp, dropped capabilities, no network,
// and CPU/memory limits — the fields already present on gradingspec.Limits).
// See DESIGN.md section 8.
package grading

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/id"
	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
	"github.com/EduCloud-Ecosystem/cairn/pkg/gradingspec"
)

// TestResult is the outcome of a single graded check.
type TestResult struct {
	Name      string  `json:"name"`
	Passed    bool    `json:"passed"`
	Points    float64 `json:"points"`     // points awarded (0 when failed)
	MaxPoints float64 `json:"max_points"` // points available
	Detail    string  `json:"detail,omitempty"`
}

// Result is the aggregate outcome of a grading run.
type Result struct {
	Score    float64      `json:"score"`
	MaxScore float64      `json:"max_score"`
	Tests    []TestResult `json:"tests"`
	Log      string       `json:"log,omitempty"`
}

// Runner executes a grading spec against a checked-out repo at dir and returns
// the score. A non-zero student exit code is a failed test, not a Runner error;
// Runner returns a non-nil error only for infrastructural failures (could not
// start the process, etc.).
type Runner interface {
	Name() string
	Run(ctx context.Context, spec gradingspec.Spec, dir string) (Result, error)
}

// Checkout materializes a submission's repository into a local directory.
type Checkout interface {
	Fetch(ctx context.Context, repo adapter.RepoRef, dir string) error
}

// Service orchestrates a grading run: check out the repo, load the spec, run it,
// and persist a GradingRun plus a Grade.
type Service struct {
	Store    store.Store
	Runner   Runner
	Checkout Checkout
	Now      func() time.Time // injectable for tests; defaults to time.Now
}

// NewService wires a grading Service.
func NewService(s store.Store, r Runner, c Checkout) *Service {
	return &Service{Store: s, Runner: r, Checkout: c}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Grade runs grading for one submission. It is the method the provisioning
// worker invokes for a JobGrade. TargetRef is the submission ID.
func (s *Service) Grade(ctx context.Context, submissionID string) error {
	sub, err := s.Store.GetSubmission(ctx, submissionID)
	if err != nil {
		return fmt.Errorf("get submission: %w", err)
	}
	if sub.Repo.Name == "" {
		return errors.New("grading: submission has no provisioned repo")
	}
	asg, err := s.Store.GetAssignment(ctx, sub.AssignmentID)
	if err != nil {
		return fmt.Errorf("get assignment: %w", err)
	}

	started := s.now()
	run := &store.GradingRun{
		ID:           id.New(),
		SubmissionID: sub.ID,
		Status:       "running",
		Runner:       s.Runner.Name(),
		StartedAt:    &started,
	}
	if err := s.Store.CreateGradingRun(ctx, run); err != nil {
		return fmt.Errorf("create grading run: %w", err)
	}

	res, gradeErr := s.execute(ctx, sub.Repo, asg.GradingSpec)
	finished := s.now()
	run.FinishedAt = &finished

	if gradeErr != nil {
		run.Status = "failed"
		run.Result, _ = json.Marshal(map[string]string{"error": gradeErr.Error()})
		_ = s.Store.UpdateGradingRun(ctx, run)
		return gradeErr
	}

	breakdown, _ := json.Marshal(res)
	run.Status = "completed"
	run.Result = breakdown
	if err := s.Store.UpdateGradingRun(ctx, run); err != nil {
		return fmt.Errorf("update grading run: %w", err)
	}

	grade := &store.Grade{
		ID:           id.New(),
		SubmissionID: sub.ID,
		Score:        res.Score,
		MaxScore:     res.MaxScore,
		Breakdown:    breakdown,
		RunID:        run.ID,
		GradedAt:     s.now(),
	}
	if err := s.Store.CreateGrade(ctx, grade); err != nil {
		return fmt.Errorf("create grade: %w", err)
	}
	return nil
}

func (s *Service) execute(ctx context.Context, repo adapter.RepoRef, specPath string) (Result, error) {
	dir, err := os.MkdirTemp("", "cairn-grade-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)

	if err := s.Checkout.Fetch(ctx, repo, dir); err != nil {
		return Result{}, fmt.Errorf("checkout: %w", err)
	}
	// Strip VCS metadata before mounting the directory into the grading container.
	// Defense-in-depth: even if a token somehow landed in .git/config, the container
	// cannot read it because the directory no longer exists.
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		return Result{}, fmt.Errorf("strip .git: %w", err)
	}
	spec, err := loadSpec(filepath.Join(dir, specPath))
	if err != nil {
		return Result{}, err
	}
	return s.Runner.Run(ctx, spec, dir)
}

// loadSpec reads and parses a JSON grading spec. (The on-disk convention is
// grading.json; YAML support is a thin add for production but is omitted here to
// keep the bundled runner dependency-free.)
func loadSpec(path string) (gradingspec.Spec, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return gradingspec.Spec{}, fmt.Errorf("read grading spec: %w", err)
	}
	var spec gradingspec.Spec
	if err := json.Unmarshal(b, &spec); err != nil {
		return gradingspec.Spec{}, fmt.Errorf("parse grading spec: %w", err)
	}
	if len(spec.Tests) == 0 {
		return gradingspec.Spec{}, errors.New("grading spec defines no tests")
	}
	for i, t := range spec.Tests {
		if t.Run == "" {
			return gradingspec.Spec{}, fmt.Errorf("grading spec test[%d] %q has empty run field", i, t.Name)
		}
	}
	return spec, nil
}

// truncate caps a string for safe storage in logs/details.
func truncate(s string) string {
	const max = 2000
	if len(s) <= max {
		return s
	}
	return s[:max] + "…(truncated)"
}
