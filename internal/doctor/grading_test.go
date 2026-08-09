// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCommander records invocations and replays scripted responses, so the
// grading checks run with no Docker and no network.
type fakeCommander struct {
	calls   [][]string
	version string
	// versionErr fails the runtime check.
	versionErr error
	// runOut is what a `run` invocation prints; runErr fails it.
	runOut string
	runErr error
	// infoOut is what `info --format {{.Runtimes}}` prints; infoErr fails it.
	// Empty infoOut stands for a daemon with no runsc registered.
	infoOut string
	infoErr error
}

func (f *fakeCommander) Run(_ context.Context, name string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if len(args) > 0 && args[0] == "version" {
		return f.version, f.versionErr
	}
	if len(args) > 0 && args[0] == "info" {
		return f.infoOut, f.infoErr
	}
	return f.runOut, f.runErr
}

// memFS satisfies ProbeFS against a real temp dir, which keeps the test honest
// about path handling without touching anything outside t.TempDir(). It ignores
// the requested parent so a config can name a production path like
// /var/lib/cairn/work without the test needing it to exist.
type memFS struct{ root string }

func (m memFS) MkdirTemp(_, pattern string) (string, error) {
	return os.MkdirTemp(m.root, pattern)
}
func (m memFS) WriteFile(path string, data []byte) error { return os.WriteFile(path, data, 0o644) }
func (m memFS) RemoveAll(path string) error              { return os.RemoveAll(path) }

func newFS(t *testing.T) memFS {
	t.Helper()
	return memFS{root: t.TempDir()}
}

func TestCheckGradingModes(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t)

	t.Run("unset warns", func(t *testing.T) {
		got := CheckGrading(ctx, GradingConfig{}, &fakeCommander{}, fs)
		if len(got) != 1 || got[0].Status != StatusWarn {
			t.Fatalf("got %+v", got)
		}
		requireFixes(t, got)
	})

	t.Run("local-exec warns about the missing sandbox", func(t *testing.T) {
		got := CheckGrading(ctx, GradingConfig{Mode: GraderLocalExec}, &fakeCommander{}, fs)
		if got[0].Status != StatusWarn || !strings.Contains(got[0].Detail, "NO sandbox") {
			t.Errorf("got %+v", got[0])
		}
	})

	t.Run("unknown mode fails", func(t *testing.T) {
		got := CheckGrading(ctx, GradingConfig{Mode: "containerish"}, &fakeCommander{}, fs)
		if got[0].Status != StatusFail {
			t.Errorf("got %+v", got[0])
		}
		requireFixes(t, got)
	})
}

func TestCheckGradingRuntimeErrorsAreActionable(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t)

	cases := map[string]struct {
		output, wantFix string
	}{
		"socket permission": {
			output:  "Got permission denied while trying to connect to the Docker daemon socket",
			wantFix: "DOCKER_GID",
		},
		"daemon down": {
			output:  "Cannot connect to the Docker daemon at unix:///var/run/docker.sock",
			wantFix: "docker.sock",
		},
		"not installed": {
			output:  `exec: "docker": executable file not found in $PATH`,
			wantFix: "install docker",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := &fakeCommander{version: c.output, versionErr: errors.New(c.output)}
			got := CheckGrading(ctx, GradingConfig{Mode: GraderContainer, Runtime: "docker"}, cmd, fs)
			if got[0].Status != StatusFail {
				t.Fatalf("got %+v", got[0])
			}
			if !strings.Contains(got[0].Fix, c.wantFix) {
				t.Errorf("fix should mention %q, got %q", c.wantFix, got[0].Fix)
			}
		})
	}
}

func TestCheckGradingImageAndMount(t *testing.T) {
	ctx := context.Background()

	t.Run("no default image warns and skips the probe", func(t *testing.T) {
		cmd := &fakeCommander{version: "27.0.0"}
		got := CheckGrading(ctx, GradingConfig{Mode: GraderContainer}, cmd, newFS(t))
		if !hasStatus(got, StatusWarn) {
			t.Fatalf("got %+v", statuses(got))
		}
		for _, call := range cmd.calls {
			if len(call) > 1 && call[1] == "run" {
				t.Error("must not attempt a mount probe without an image to probe with")
			}
		}
	})

	t.Run("visible workdir passes", func(t *testing.T) {
		cmd := &fakeCommander{version: "27.0.0", runOut: probeSentinel + "\n"}
		got := CheckGrading(ctx, GradingConfig{
			Mode: GraderContainer, Image: "cairn/grader:latest", WorkDir: t.TempDir(),
		}, cmd, newFS(t))
		for _, r := range got {
			if r.Status != StatusOK {
				t.Errorf("unexpected %v: %s", r.Status, r.Detail)
			}
		}

		// The probe must mount the way the real runner mounts: -v <hostdir>:/work.
		var probed bool
		for _, call := range cmd.calls {
			for i, a := range call {
				if a == "-v" && i+1 < len(call) && strings.HasSuffix(call[i+1], ":/work") {
					probed = true
				}
			}
		}
		if !probed {
			t.Errorf("probe did not mount at /work like internal/grading does: %v", cmd.calls)
		}
	})

	// The container blocker: a path that exists only inside the Cairn container is
	// empty when the host daemon mounts it, so every grading run finds no checkout.
	t.Run("invisible workdir fails with the identical-path fix", func(t *testing.T) {
		cmd := &fakeCommander{version: "27.0.0", runOut: ""} // mounted, but empty
		got := CheckGrading(ctx, GradingConfig{
			Mode: GraderContainer, Image: "cairn/grader:latest", WorkDir: "/var/lib/cairn/work",
		}, cmd, newFS(t))

		var probe Result
		for _, r := range got {
			if r.Name == "grading workdir" {
				probe = r
			}
		}
		if probe.Status != StatusFail {
			t.Fatalf("expected a failure, got %+v", got)
		}
		for _, want := range []string{"-v /var/lib/cairn/work:/var/lib/cairn/work", "HOST"} {
			if !strings.Contains(probe.Fix, want) {
				t.Errorf("fix should contain %q, got:\n%s", want, probe.Fix)
			}
		}
	})

	t.Run("unwritable workdir fails before probing", func(t *testing.T) {
		cmd := &fakeCommander{version: "27.0.0"}
		missing := filepath.Join(t.TempDir(), "does-not-exist")
		got := CheckGrading(ctx, GradingConfig{
			Mode: GraderContainer, Image: "img", WorkDir: missing,
		}, cmd, memFS{root: missing})
		if !hasStatus(got, StatusFail) {
			t.Errorf("got %+v", statuses(got))
		}
		requireFixes(t, got)
	})
}
