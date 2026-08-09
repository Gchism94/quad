// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// findResult returns the first result whose Name matches, so these tests assert
// on the isolation line specifically rather than on ordering.
func findResult(results []Result, name string) (Result, bool) {
	for _, r := range results {
		if r.Name == name {
			return r, true
		}
	}
	return Result{}, false
}

// Docker's gVisor detection is CC-CA1's original, already-shipped path. Adding
// podman's branch must refine podman's case only. This test states Docker's
// three outcomes as an explicit regression guard, separately from the podman
// work, so a future edit to the shared function cannot quietly change them.
func TestDockerGVisorDetectionIsUnchanged(t *testing.T) {
	ctx := context.Background()
	const withRunsc = "map[runc:{runc []} runsc:{/usr/bin/runsc []}]"
	const withoutRunsc = "map[runc:{runc []}]"

	cases := []struct {
		name       string
		infoOut    string
		infoErr    error
		wantStatus Status
	}{
		{"runsc registered", withRunsc, nil, StatusOK},
		{"runsc absent", withoutRunsc, nil, StatusFail},
		{"daemon unqueryable", "", errors.New("permission denied"), StatusFail},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFS(t)
			cmd := &fakeCommander{
				version: "27.0.0", runOut: probeSentinel,
				infoOut: tc.infoOut, infoErr: tc.infoErr,
			}
			got := CheckGrading(ctx, GradingConfig{
				Mode: GraderContainer, Runtime: "docker", Image: "img", Isolation: IsolationGVisor,
			}, cmd, fs)

			res, ok := findResult(got, "grading isolation")
			if !ok {
				t.Fatalf("no isolation result in %+v", statuses(got))
			}
			if res.Status != tc.wantStatus {
				t.Errorf("status = %v, want %v (Docker's behaviour must not change); detail: %s",
					res.Status, tc.wantStatus, res.Detail)
			}
			// Docker must still be queried — the podman branch must not have
			// short-circuited the real check.
			queried := false
			for _, call := range cmd.calls {
				if len(call) > 1 && call[1] == "info" {
					queried = true
				}
			}
			if !queried {
				t.Error("Docker's registered-runtimes query was not run")
			}
		})
	}
}

func TestCheckGradingIsolationTier(t *testing.T) {
	ctx := context.Background()

	// A live runsc is never required: the daemon's registered-runtime list is
	// scripted, so this runs identically on a CI box with no gVisor installed.
	const dockerRuntimesWithRunsc = "map[runc:{runc []} runsc:{/usr/bin/runsc []}]"
	const dockerRuntimesWithout = "map[runc:{runc []}]"

	t.Run("default tier reports posture without failing", func(t *testing.T) {
		fs := newFS(t)
		cmd := &fakeCommander{version: "27.0.0", runOut: probeSentinel}
		got := CheckGrading(ctx, GradingConfig{
			Mode: GraderContainer, Image: "img", // Isolation unset
		}, cmd, fs)

		res, ok := findResult(got, "grading isolation")
		if !ok {
			t.Fatalf("no isolation result in %+v", statuses(got))
		}
		// Not opting in is not a misconfiguration, so this must not fail the run.
		if res.Status != StatusOK {
			t.Errorf("default tier status = %v, want OK (it is the documented default)", res.Status)
		}
		// It still has to be legible as a security posture at a glance.
		for _, want := range []string{"shared host kernel", "CAIRN_GRADER_ISOLATION=gvisor"} {
			if !strings.Contains(res.Detail, want) {
				t.Errorf("detail should mention %q, got: %s", want, res.Detail)
			}
		}
	})

	t.Run("explicit shared tier behaves like the default", func(t *testing.T) {
		fs := newFS(t)
		cmd := &fakeCommander{version: "27.0.0", runOut: probeSentinel}
		got := CheckGrading(ctx, GradingConfig{
			Mode: GraderContainer, Image: "img", Isolation: IsolationShared,
		}, cmd, fs)

		res, _ := findResult(got, "grading isolation")
		if res.Status != StatusOK {
			t.Errorf("shared tier status = %v, want OK", res.Status)
		}
	})

	t.Run("gvisor available reports the kernel boundary", func(t *testing.T) {
		fs := newFS(t)
		cmd := &fakeCommander{
			version: "27.0.0", runOut: probeSentinel,
			infoOut: dockerRuntimesWithRunsc,
		}
		got := CheckGrading(ctx, GradingConfig{
			Mode: GraderContainer, Image: "img", Isolation: IsolationGVisor,
		}, cmd, fs)

		res, ok := findResult(got, "grading isolation")
		if !ok {
			t.Fatalf("no isolation result in %+v", statuses(got))
		}
		if res.Status != StatusOK {
			t.Errorf("status = %v, want OK when runsc is registered; detail: %s", res.Status, res.Detail)
		}
		if !strings.Contains(res.Detail, "gVisor") {
			t.Errorf("detail should name gVisor, got: %s", res.Detail)
		}
	})

	t.Run("gvisor requested but unavailable fails with a fix", func(t *testing.T) {
		fs := newFS(t)
		cmd := &fakeCommander{
			version: "27.0.0", runOut: probeSentinel,
			infoOut: dockerRuntimesWithout, // daemon knows runc only
		}
		got := CheckGrading(ctx, GradingConfig{
			Mode: GraderContainer, Image: "img", Isolation: IsolationGVisor,
		}, cmd, fs)

		res, ok := findResult(got, "grading isolation")
		if !ok {
			t.Fatalf("no isolation result in %+v", statuses(got))
		}
		// This is the case that must not be a warning: the operator asked for a
		// boundary they are not getting.
		if res.Status != StatusFail {
			t.Errorf("status = %v, want Fail when a requested tier is unavailable", res.Status)
		}
		if res.Fix == "" {
			t.Error("a failing check must print the fix")
		}
		for _, want := range []string{"gvisor.dev", "runsc"} {
			if !strings.Contains(res.Fix, want) {
				t.Errorf("fix should mention %q, got:\n%s", want, res.Fix)
			}
		}
		// The message should make clear grading refuses rather than downgrades.
		if !strings.Contains(res.Detail, "refuse") {
			t.Errorf("detail should say grading refuses rather than falling back, got: %s", res.Detail)
		}
	})

	t.Run("daemon that cannot be queried fails rather than assuming", func(t *testing.T) {
		fs := newFS(t)
		cmd := &fakeCommander{
			version: "27.0.0", runOut: probeSentinel,
			infoErr: errors.New("permission denied"),
		}
		got := CheckGrading(ctx, GradingConfig{
			Mode: GraderContainer, Image: "img", Isolation: IsolationGVisor,
		}, cmd, fs)

		res, _ := findResult(got, "grading isolation")
		if res.Status != StatusFail {
			t.Errorf("status = %v, want Fail when the runtime list is unreadable "+
				"(assuming availability would be the silent-downgrade bug)", res.Status)
		}
	})

	t.Run("unknown tier fails with the valid values", func(t *testing.T) {
		fs := newFS(t)
		cmd := &fakeCommander{version: "27.0.0", runOut: probeSentinel}
		got := CheckGrading(ctx, GradingConfig{
			Mode: GraderContainer, Image: "img", Isolation: "gvsior", // typo
		}, cmd, fs)

		res, _ := findResult(got, "grading isolation")
		if res.Status != StatusFail {
			t.Errorf("status = %v, want Fail for an unknown tier", res.Status)
		}
		if !strings.Contains(res.Fix, IsolationGVisor) || !strings.Contains(res.Fix, IsolationShared) {
			t.Errorf("fix should list the valid values, got:\n%s", res.Fix)
		}
	})

	// Podman is daemonless: runtimes come from containers.conf and are chosen
	// per invocation, and `podman info` exposes only .Host.ociRuntime — the
	// runtime in use — with no list of available alternatives. Docker's query
	// therefore returns nothing under podman, which would misreport a working
	// gVisor setup as "runsc not registered". Say "not checked" instead.
	t.Run("podman says the check is not implemented rather than misreporting", func(t *testing.T) {
		fs := newFS(t)
		// infoOut is empty, exactly what Docker's query yields against podman.
		cmd := &fakeCommander{version: "5.2.0", runOut: probeSentinel}
		got := CheckGrading(ctx, GradingConfig{
			Mode: GraderContainer, Runtime: "podman", Image: "img", Isolation: IsolationGVisor,
		}, cmd, fs)

		res, ok := findResult(got, "grading isolation")
		if !ok {
			t.Fatalf("no isolation result in %+v", statuses(got))
		}
		// Warn, not Fail: grading passes --runtime runsc per container, which is
		// podman's own mechanism, so a correct setup must not be blocked. Warn,
		// not OK: nothing was actually verified.
		if res.Status != StatusWarn {
			t.Errorf("podman status = %v, want Warn (unverifiable, not broken and not confirmed)", res.Status)
		}
		// The message must say the check did not happen...
		for _, want := range []string{"not implemented for podman", "NOT verified"} {
			if !strings.Contains(res.Detail, want) {
				t.Errorf("detail should say the check did not run (%q), got: %s", want, res.Detail)
			}
		}
		// ...and must NOT claim runsc was looked for and missing.
		for _, forbidden := range []string{"no \"runsc\" runtime registered", "not registered"} {
			if strings.Contains(res.Detail, forbidden) {
				t.Errorf("detail misleadingly claims runsc was checked (%q): %s", forbidden, res.Detail)
			}
		}
		if res.Fix == "" {
			t.Error("a non-OK check must print the fix")
		}
		if !strings.Contains(res.Fix, "--runtime runsc") {
			t.Errorf("fix should show how to verify manually, got:\n%s", res.Fix)
		}
		// Docker's query must not have been run against podman at all.
		for _, call := range cmd.calls {
			if len(call) > 1 && call[1] == "info" {
				t.Errorf("ran Docker's `info` query against podman: %v", call)
			}
		}
	})

	// Podman with the default tier is unaffected by the branch above.
	t.Run("podman on the default tier is unchanged", func(t *testing.T) {
		fs := newFS(t)
		cmd := &fakeCommander{version: "5.2.0", runOut: probeSentinel}
		got := CheckGrading(ctx, GradingConfig{
			Mode: GraderContainer, Runtime: "podman", Image: "img",
		}, cmd, fs)

		res, _ := findResult(got, "grading isolation")
		if res.Status != StatusOK {
			t.Errorf("podman default tier status = %v, want OK", res.Status)
		}
		if !strings.Contains(res.Detail, "shared host kernel") {
			t.Errorf("detail = %s", res.Detail)
		}
	})

	// The isolation check must not be reached before the runtime itself is known
	// to work, or its failure would mask the real problem.
	t.Run("unusable runtime short-circuits before the isolation check", func(t *testing.T) {
		fs := newFS(t)
		cmd := &fakeCommander{versionErr: errors.New("cannot connect")}
		got := CheckGrading(ctx, GradingConfig{
			Mode: GraderContainer, Image: "img", Isolation: IsolationGVisor,
		}, cmd, fs)

		if _, ok := findResult(got, "grading isolation"); ok {
			t.Error("isolation was checked despite an unusable runtime; " +
				"the runtime failure is the actionable one")
		}
	})
}
