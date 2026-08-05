// SPDX-License-Identifier: AGPL-3.0-or-later

package grading

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/pkg/gradingspec"
)

// ExecRunner runs a grading spec by executing each test command in a shell on
// the host, in the checkout directory.
//
// ┌─────────────────────────────────────────────────────────────────────────┐
// │ UNSAFE FOR UNTRUSTED CODE. This runner provides NO isolation: it does not │
// │ sandbox the filesystem, drop privileges, restrict network egress, or cap  │
// │ CPU/memory. It enforces only a per-test wall-clock timeout. It is intended │
// │ for local development and trusted course material ONLY. Do not point it at │
// │ untrusted student submissions in a shared or production environment —      │
// │ provide a container/microVM Runner that enforces gradingspec.Limits.       │
// └─────────────────────────────────────────────────────────────────────────┘
type ExecRunner struct {
	Shell          string        // default "sh"
	DefaultTimeout time.Duration // per-test fallback; default 30s
}

// NewExecRunner returns an ExecRunner with defaults.
func NewExecRunner() *ExecRunner { return &ExecRunner{} }

func (r *ExecRunner) Name() string { return "local-exec" }

func (r *ExecRunner) shell() string {
	if r.Shell != "" {
		return r.Shell
	}
	return "sh"
}

func (r *ExecRunner) specTimeout(spec gradingspec.Spec) time.Duration {
	if spec.Limits.Timeout > 0 {
		return spec.Limits.Timeout
	}
	if r.DefaultTimeout > 0 {
		return r.DefaultTimeout
	}
	return 30 * time.Second
}

func (r *ExecRunner) testTimeout(spec gradingspec.Spec, t gradingspec.Test) time.Duration {
	if t.Limits != nil && t.Limits.Timeout > 0 {
		return t.Limits.Timeout
	}
	return r.specTimeout(spec)
}

// Run executes setup steps then each test, scoring by exit code or stdout match.
func (r *ExecRunner) Run(ctx context.Context, spec gradingspec.Spec, dir string) (Result, error) {
	res := Result{MaxScore: spec.MaxScore()}

	for _, step := range spec.Setup {
		stdout, stderr, err := r.runCmd(ctx, dir, step, r.specTimeout(spec))
		if err != nil {
			// A failed setup step means the submission cannot be graded; every
			// test scores zero. This is a graded outcome, not a Runner error.
			res.Log = truncate("setup step failed: " + step + "\n" + stdout + stderr)
			for _, t := range spec.Tests {
				res.Tests = append(res.Tests, TestResult{Name: t.Name, MaxPoints: t.Points, Detail: "skipped: setup failed"})
			}
			return res, nil
		}
	}

	for _, t := range spec.Tests {
		tr := TestResult{Name: t.Name, MaxPoints: t.Points}
		stdout, stderr, runErr := r.runCmd(ctx, dir, t.Run, r.testTimeout(spec, t))

		passed := false
		if t.Match != nil {
			got, exp := stdout, t.Match.Expected
			if t.Match.Trim {
				got, exp = strings.TrimSpace(got), strings.TrimSpace(exp)
			}
			passed = got == exp
			if !passed {
				tr.Detail = "stdout did not match expected"
			}
		} else {
			passed = runErr == nil
			if !passed {
				tr.Detail = "command exited non-zero"
			}
		}

		if passed {
			tr.Passed = true
			tr.Points = t.Points
			res.Score += t.Points
		} else {
			snippet := stdout
			if strings.TrimSpace(snippet) == "" {
				snippet = stderr
			}
			if snippet != "" {
				tr.Detail = tr.Detail + ": " + truncate(snippet)
			}
		}
		res.Tests = append(res.Tests, tr)
	}
	return res, nil
}

func (r *ExecRunner) runCmd(ctx context.Context, dir, command string, timeout time.Duration) (stdout, stderr string, err error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, r.shell(), "-c", command)
	cmd.Dir = dir
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err = cmd.Run()
	return so.String(), se.String(), err
}
