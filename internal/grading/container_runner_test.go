// SPDX-License-Identifier: AGPL-3.0-or-later

package grading

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/pkg/gradingspec"
)

type fakeCall struct {
	name    string
	args    []string
	timeout time.Duration
}

// fakeRunner records invocations and returns results keyed by the command (the
// final argument of a `run ... sh -c <command>` invocation).
type fakeRunner struct {
	calls     []fakeCall
	byCommand map[string]cmdResult
	startErr  error
}

func (f *fakeRunner) run(_ context.Context, name string, args []string, timeout time.Duration) (cmdResult, error) {
	f.calls = append(f.calls, fakeCall{name: name, args: args, timeout: timeout})
	if f.startErr != nil {
		return cmdResult{}, f.startErr
	}
	cmd := args[len(args)-1]
	if res, ok := f.byCommand[cmd]; ok {
		return res, nil
	}
	return cmdResult{exitCode: 0}, nil
}

// flagValue returns the argument following flag, or "".
func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func runContainer(t *testing.T, r *ContainerRunner, spec gradingspec.Spec) (Result, *fakeRunner) {
	t.Helper()
	res, err := r.Run(context.Background(), spec, "/host/checkout")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res, r.exec.(*fakeRunner)
}

func TestContainerArgsAreHardened(t *testing.T) {
	fr := &fakeRunner{}
	// Set User explicitly so the test is not sensitive to which uid runs the test suite.
	r := &ContainerRunner{Runtime: "docker", DefaultImage: "grader:1", User: "65534:65534", exec: fr}
	_, _ = runContainer(t, r, gradingspec.Spec{Tests: []gradingspec.Test{{Name: "t", Run: "true", Points: 1}}})

	if len(fr.calls) != 1 {
		t.Fatalf("expected 1 invocation, got %d", len(fr.calls))
	}
	a := fr.calls[0].args
	if fr.calls[0].name != "docker" {
		t.Fatalf("runtime = %q, want docker", fr.calls[0].name)
	}
	for _, want := range []string{"run", "--rm", "--read-only"} {
		if !hasArg(a, want) {
			t.Errorf("args missing %q", want)
		}
	}
	checks := map[string]string{
		"--network":      "none",
		"--cap-drop":     "ALL",
		"--security-opt": "no-new-privileges",
		"--memory":       "512m", // fail-safe default
		"--memory-swap":  "512m",
		"--cpus":         "1",
		"--pids-limit":   "256",
		"--workdir":      "/work",
		"--user":         "65534:65534",
		"--env":          "HOME=/tmp",
	}
	for flag, val := range checks {
		if got := flagValue(a, flag); got != val {
			t.Errorf("%s = %q, want %q", flag, got, val)
		}
	}
	if got := flagValue(a, "-v"); got != "/host/checkout:/work" {
		t.Errorf("mount = %q", got)
	}
	// command tail
	if a[len(a)-3] != "sh" || a[len(a)-2] != "-c" || a[len(a)-1] != "true" {
		t.Errorf("command tail = %v", a[len(a)-3:])
	}
}

func TestContainerScoring(t *testing.T) {
	fr := &fakeRunner{byCommand: map[string]cmdResult{
		"pass-exit": {exitCode: 0},
		"fail-exit": {exitCode: 1},
		"echo hi":   {stdout: "hi\n"},
		"echo no":   {stdout: "no\n"},
		"sleep":     {timedOut: true},
	}}
	r := &ContainerRunner{DefaultImage: "img", exec: fr}
	spec := gradingspec.Spec{Tests: []gradingspec.Test{
		{Name: "a", Run: "pass-exit", Points: 5},
		{Name: "b", Run: "fail-exit", Points: 4},
		{Name: "c", Run: "echo hi", Points: 3, Match: &gradingspec.OutputMatch{Expected: "hi", Trim: true}},
		{Name: "d", Run: "echo no", Points: 2, Match: &gradingspec.OutputMatch{Expected: "yes", Trim: true}},
		{Name: "e", Run: "sleep", Points: 6},
	}}
	res, _ := runContainer(t, r, spec)

	if res.MaxScore != 20 {
		t.Fatalf("MaxScore = %v, want 20", res.MaxScore)
	}
	if res.Score != 8 { // 5 + 3
		t.Fatalf("Score = %v, want 8", res.Score)
	}
	want := map[string]bool{"a": true, "b": false, "c": true, "d": false, "e": false}
	var timeoutSeen bool
	for _, tr := range res.Tests {
		if tr.Passed != want[tr.Name] {
			t.Errorf("test %q passed=%v, want %v", tr.Name, tr.Passed, want[tr.Name])
		}
		if tr.Name == "e" && tr.Detail == "timed out" {
			timeoutSeen = true
		}
	}
	if !timeoutSeen {
		t.Error("timed-out test should report 'timed out'")
	}
	// A timeout triggers a best-effort kill: extra invocation beyond the 5 steps.
	if len(fr.calls) != 6 {
		t.Fatalf("expected 6 invocations (5 steps + 1 kill), got %d", len(fr.calls))
	}
	if last := fr.calls[5].args; last[0] != "kill" {
		t.Errorf("expected a kill invocation, got %v", last)
	}
}

func TestContainerSetupFailureZeroes(t *testing.T) {
	fr := &fakeRunner{byCommand: map[string]cmdResult{"build": {exitCode: 2}}}
	r := &ContainerRunner{DefaultImage: "img", exec: fr}
	res, _ := runContainer(t, r, gradingspec.Spec{
		Setup: []string{"build"},
		Tests: []gradingspec.Test{{Name: "t", Run: "true", Points: 10}},
	})
	if res.Score != 0 || res.MaxScore != 10 {
		t.Fatalf("score=%v max=%v, want 0/10", res.Score, res.MaxScore)
	}
	// Only setup ran; the test step was skipped.
	if len(fr.calls) != 1 {
		t.Fatalf("expected 1 invocation (setup only), got %d", len(fr.calls))
	}
}

func TestContainerInfraErrorAborts(t *testing.T) {
	fr := &fakeRunner{startErr: errors.New("docker: command not found")}
	r := &ContainerRunner{DefaultImage: "img", exec: fr}
	if _, err := r.Run(context.Background(), gradingspec.Spec{Tests: []gradingspec.Test{{Name: "t", Run: "true", Points: 1}}}, "/d"); err == nil {
		t.Fatal("expected an infrastructure error to abort the run")
	}
}

func TestContainerNoImageErrors(t *testing.T) {
	r := &ContainerRunner{exec: &fakeRunner{}}
	if _, err := r.Run(context.Background(), gradingspec.Spec{Tests: []gradingspec.Test{{Name: "t", Run: "true"}}}, "/d"); err == nil {
		t.Fatal("expected an error when no image is configured")
	}
}

func TestContainerRestrictedNetwork(t *testing.T) {
	// With a configured network, restricted attaches it.
	fr := &fakeRunner{}
	r := &ContainerRunner{DefaultImage: "img", RestrictedNetwork: "egress", exec: fr}
	_, _ = runContainer(t, r, gradingspec.Spec{
		Limits: gradingspec.Limits{Network: gradingspec.NetworkRestricted},
		Tests:  []gradingspec.Test{{Name: "t", Run: "true", Points: 1}},
	})
	if got := flagValue(fr.calls[0].args, "--network"); got != "egress" {
		t.Fatalf("--network = %q, want egress", got)
	}

	// Without one, restricted fails safe to none.
	fr2 := &fakeRunner{}
	r2 := &ContainerRunner{DefaultImage: "img", exec: fr2}
	_, _ = runContainer(t, r2, gradingspec.Spec{
		Limits: gradingspec.Limits{Network: gradingspec.NetworkRestricted},
		Tests:  []gradingspec.Test{{Name: "t", Run: "true", Points: 1}},
	})
	if got := flagValue(fr2.calls[0].args, "--network"); got != "none" {
		t.Fatalf("restricted without a network = %q, want none (fail-safe)", got)
	}
}

func TestContainerPerTestLimitOverride(t *testing.T) {
	fr := &fakeRunner{}
	r := &ContainerRunner{DefaultImage: "img", exec: fr}
	_, _ = runContainer(t, r, gradingspec.Spec{
		Limits: gradingspec.Limits{MemoryMB: 256, Timeout: 10 * time.Second},
		Tests: []gradingspec.Test{{
			Name: "t", Run: "true", Points: 1,
			Limits: &gradingspec.Limits{MemoryMB: 128, Timeout: 3 * time.Second},
		}},
	})
	if got := flagValue(fr.calls[0].args, "--memory"); got != "128m" {
		t.Fatalf("--memory = %q, want 128m (per-test override)", got)
	}
	if fr.calls[0].timeout != 3*time.Second {
		t.Fatalf("timeout = %v, want 3s (per-test override)", fr.calls[0].timeout)
	}
}

func TestContainerImageFromSpecWins(t *testing.T) {
	fr := &fakeRunner{}
	r := &ContainerRunner{DefaultImage: "fallback:1", exec: fr}
	_, _ = runContainer(t, r, gradingspec.Spec{
		Image: "python:3.12",
		Tests: []gradingspec.Test{{Name: "t", Run: "true", Points: 1}},
	})
	args := fr.calls[0].args
	// image is the arg just before the `sh -c <cmd>` tail
	if img := args[len(args)-4]; img != "python:3.12" {
		t.Fatalf("image = %q, want python:3.12", img)
	}
}

// TestContainerDefaultUserIsHostUID verifies that when User is not set the runner
// passes the server process's own uid:gid to --user so the bind-mounted checkout
// directory (owned by the server process) is writable inside the container.
func TestContainerDefaultUserIsHostUID(t *testing.T) {
	fr := &fakeRunner{}
	r := &ContainerRunner{DefaultImage: "img", exec: fr} // User deliberately unset
	_, _ = runContainer(t, r, gradingspec.Spec{Tests: []gradingspec.Test{{Name: "t", Run: "true", Points: 1}}})

	want := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	if got := flagValue(fr.calls[0].args, "--user"); got != want {
		t.Fatalf("--user = %q, want host uid:gid %q", got, want)
	}
}
