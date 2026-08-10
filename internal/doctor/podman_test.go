// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// runtimeFix's advice must match the runtime that failed. Docker's answer for a
// permission error is "join the docker group / set DOCKER_GID"; podman is
// daemonless and rootless, where that advice points at things that do not
// exist. The Docker rows are a regression guard on CC-CA10's behaviour.
func TestRuntimeFixIsRuntimeSpecific(t *testing.T) {
	cases := []struct {
		name       string
		runtime    string
		output     string
		err        error
		wantAll    []string
		wantNoneOf []string
	}{
		{
			name:    "docker permission denied still advises the docker group",
			runtime: "docker",
			output:  "permission denied while trying to connect to the Docker daemon socket",
			err:     errors.New("exit status 1"),
			wantAll: []string{"DOCKER_GID", "docker"},
		},
		{
			name:    "docker cannot connect still advises the daemon and socket",
			runtime: "docker",
			output:  "cannot connect to the Docker daemon",
			err:     errors.New("exit status 1"),
			wantAll: []string{"systemctl", "docker.sock"},
		},
		{
			name:    "docker missing binary still advises installing docker",
			runtime: "docker",
			output:  "executable file not found in $PATH",
			err:     errors.New("exec: \"docker\""),
			wantAll: []string{"install docker"},
		},
		{
			name:    "podman permission denied advises the user socket, not a group",
			runtime: "podman",
			output:  "permission denied",
			err:     errors.New("exit status 125"),
			wantAll: []string{"systemctl --user", "podman.socket"},
			// The Docker answer is actively wrong here: rootless podman has no
			// daemon to join a group for.
			wantNoneOf: []string{"DOCKER_GID", "docker group"},
		},
		{
			name:       "podman cannot connect advises socket activation",
			runtime:    "podman",
			output:     "cannot connect to podman",
			err:        errors.New("exit status 125"),
			wantAll:    []string{"daemonless", "systemctl --user enable --now podman.socket"},
			wantNoneOf: []string{"DOCKER_GID"},
		},
		{
			name:       "podman on macOS is told to start a machine",
			runtime:    "podman",
			output:     "podman machine is not running",
			err:        errors.New("exit status 125"),
			wantAll:    []string{"podman machine init", "podman machine start"},
			wantNoneOf: []string{"DOCKER_GID"},
		},
		{
			name:       "podman missing binary advises installing podman",
			runtime:    "podman",
			output:     "executable file not found in $PATH",
			err:        errors.New("exec: \"podman\""),
			wantAll:    []string{"install podman"},
			wantNoneOf: []string{"install docker,"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runtimeFix(tc.runtime, tc.output, tc.err)
			for _, want := range tc.wantAll {
				if !strings.Contains(got, want) {
					t.Errorf("fix should mention %q, got:\n%s", want, got)
				}
			}
			for _, bad := range tc.wantNoneOf {
				if strings.Contains(got, bad) {
					t.Errorf("fix should NOT mention %q (wrong runtime's advice), got:\n%s", bad, got)
				}
			}
		})
	}
}

// The mount round trip is a shared code path on purpose: every flag it uses
// exists with the same meaning in podman. This pins podman as supported so a
// future edit cannot make it Docker-only by accident — the same guard
// TestDockerGVisorDetectionIsUnchanged provides for the isolation check.
func TestMountRoundTripSupportsPodman(t *testing.T) {
	ctx := context.Background()

	t.Run("podman round trip succeeds with the same flags", func(t *testing.T) {
		fs := newFS(t)
		cmd := &fakeCommander{version: "5.2.0", runOut: probeSentinel}
		got := CheckGrading(ctx, GradingConfig{
			Mode: GraderContainer, Runtime: "podman", Image: "img",
		}, cmd, fs)

		res, ok := findResult(got, "grading workdir")
		if !ok {
			t.Fatalf("no workdir result in %+v", statuses(got))
		}
		if res.Status != StatusOK {
			t.Errorf("status = %v, want OK; detail: %s", res.Status, res.Detail)
		}

		// The probe must have been issued through podman, with the flags the
		// grading runner actually uses.
		var probe []string
		for _, call := range cmd.calls {
			if len(call) > 1 && call[0] == "podman" && call[1] == "run" {
				probe = call
			}
		}
		if probe == nil {
			t.Fatalf("no podman run probe was issued; calls: %v", cmd.calls)
		}
		for _, want := range []string{"--rm", "--network", "none", "-v"} {
			found := false
			for _, a := range probe {
				if a == want {
					found = true
				}
			}
			if !found {
				t.Errorf("probe missing %q: %v", want, probe)
			}
		}
	})

	// A failed podman round trip should mention SELinux relabelling and the
	// rootless UID mapping — the two ways this fails on podman but rarely on
	// Docker — instead of only Docker's same-path advice.
	t.Run("podman failure advises relabelling and uid mapping", func(t *testing.T) {
		fs := newFS(t)
		cmd := &fakeCommander{version: "5.2.0", runOut: "", runErr: errors.New("exit status 125")}
		got := CheckGrading(ctx, GradingConfig{
			Mode: GraderContainer, Runtime: "podman", Image: "img", WorkDir: "/var/lib/cairn/work",
		}, cmd, fs)

		res, _ := findResult(got, "grading workdir")
		if res.Status != StatusFail {
			t.Fatalf("status = %v, want Fail", res.Status)
		}
		for _, want := range []string{"SELinux", ":z", "rootless podman maps UIDs"} {
			if !strings.Contains(res.Fix, want) {
				t.Errorf("fix should mention %q, got:\n%s", want, res.Fix)
			}
		}
	})

	// Docker's failure advice is unchanged by the podman branch.
	t.Run("docker failure advice is unchanged", func(t *testing.T) {
		fs := newFS(t)
		cmd := &fakeCommander{version: "27.0.0", runOut: "", runErr: errors.New("exit status 1")}
		got := CheckGrading(ctx, GradingConfig{
			Mode: GraderContainer, Runtime: "docker", Image: "img", WorkDir: "/var/lib/cairn/work",
		}, cmd, fs)

		res, _ := findResult(got, "grading workdir")
		if res.Status != StatusFail {
			t.Fatalf("status = %v, want Fail", res.Status)
		}
		if !strings.Contains(res.Fix, "-v /var/lib/cairn/work:/var/lib/cairn/work") {
			t.Errorf("docker fix lost its same-path advice:\n%s", res.Fix)
		}
		// podman-only advice must not leak into Docker's message.
		if strings.Contains(res.Fix, "SELinux") {
			t.Errorf("docker fix picked up podman's SELinux advice:\n%s", res.Fix)
		}
	})
}
