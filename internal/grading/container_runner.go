// SPDX-License-Identifier: AGPL-3.0-or-later

package grading

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/id"
	"github.com/EduCloud-Ecosystem/cairn/pkg/gradingspec"
)

// ContainerRunner executes a grading spec inside a container, enforcing the
// resource and network limits that the host-level ExecRunner cannot. It shells
// out to a container runtime CLI (docker or podman) rather than linking an SDK,
// so it adds no dependency to the build.
//
// Isolation applied to every step (fail-safe: limits are forced even if the spec
// omits them):
//   - --network none by default (egress denied). NetworkRestricted attaches the
//     operator-provided RestrictedNetwork; if none is configured it falls back to
//     none, so "restricted" never silently means "open".
//   - --memory / --memory-swap (no swap escape), --cpus, --pids-limit.
//   - --cap-drop ALL, --security-opt no-new-privileges, --read-only rootfs.
//   - a writable /work bind mount (the throwaway checkout) and a /tmp tmpfs; the
//     rest of the filesystem is read-only.
//   - runs as the server's own uid:gid by default (CAIRN_GRADER_USER overrides).
//
// The host performs the checkout (cloning a repo is not code execution); only the
// commands from the spec run inside the container, against the mounted clone.
// Bind-mounting a host path requires a local runtime daemon.
//
// Setup-level changes persist between steps only within the mounted checkout, so
// the toolchain belongs in the image; setup should build into the working tree.
type ContainerRunner struct {
	Runtime           string   // "docker" (default) or "podman"
	DefaultImage      string   // used when a spec sets no image
	RestrictedNetwork string   // runtime network name for NetworkRestricted
	User              string   // --user value; default is the server's own uid:gid (set CAIRN_GRADER_USER to override)
	ExtraArgs         []string // additional runtime args (advanced)

	DefaultTimeout  time.Duration // per-step fallback; default 30s
	DefaultMemoryMB int           // default 512
	DefaultCPUs     float64       // default 1.0
	DefaultPids     int           // default 256

	exec commandRunner // injected in tests; nil -> real exec
}

// NewContainerRunner returns a ContainerRunner with the given default image.
func NewContainerRunner(image string) *ContainerRunner {
	return &ContainerRunner{DefaultImage: image}
}

func (r *ContainerRunner) Name() string { return "container" }

// cmdResult is the outcome of one runtime invocation. err (from commandRunner)
// is reserved for failures to launch the runtime at all; a non-zero exit or a
// timeout is a normal grading outcome carried here.
type cmdResult struct {
	stdout, stderr string
	exitCode       int
	timedOut       bool
}

type commandRunner interface {
	run(ctx context.Context, name string, args []string, timeout time.Duration) (cmdResult, error)
}

type execCommandRunner struct{}

func (execCommandRunner) run(ctx context.Context, name string, args []string, timeout time.Duration) (cmdResult, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	res := cmdResult{stdout: so.String(), stderr: se.String()}
	if cctx.Err() == context.DeadlineExceeded {
		res.timedOut = true
		return res, nil
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.exitCode = ee.ExitCode()
			return res, nil
		}
		return res, err // could not start the runtime (e.g., binary missing)
	}
	return res, nil
}

func (r *ContainerRunner) runner() commandRunner {
	if r.exec != nil {
		return r.exec
	}
	return execCommandRunner{}
}

func (r *ContainerRunner) runtime() string {
	if r.Runtime != "" {
		return r.Runtime
	}
	return "docker"
}

func (r *ContainerRunner) user() string {
	if r.User != "" {
		return r.User
	}
	// Default to the host process's uid:gid so that the bind-mounted checkout
	// directory (created by the server and owned by the server user) is writable
	// inside the container. The container is still strongly isolated by
	// --cap-drop ALL / --security-opt no-new-privileges / --read-only / --network none.
	return fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
}

type resolvedLimits struct {
	timeout  time.Duration
	memoryMB int
	cpus     float64
	pids     int
	network  gradingspec.NetworkPolicy
}

// resolveLimits merges spec-level and (optional) test-level limits, then forces
// fail-safe defaults for anything left unset.
func (r *ContainerRunner) resolveLimits(spec gradingspec.Spec, test *gradingspec.Test) resolvedLimits {
	lim := spec.Limits
	if test != nil && test.Limits != nil {
		o := *test.Limits
		if o.Timeout > 0 {
			lim.Timeout = o.Timeout
		}
		if o.MemoryMB > 0 {
			lim.MemoryMB = o.MemoryMB
		}
		if o.CPUs > 0 {
			lim.CPUs = o.CPUs
		}
		if o.Network != "" {
			lim.Network = o.Network
		}
	}
	out := resolvedLimits{
		timeout:  lim.Timeout,
		memoryMB: lim.MemoryMB,
		cpus:     lim.CPUs,
		network:  lim.Network,
		pids:     r.DefaultPids,
	}
	if out.timeout <= 0 {
		out.timeout = r.DefaultTimeout
	}
	if out.timeout <= 0 {
		out.timeout = 30 * time.Second
	}
	if out.memoryMB <= 0 {
		out.memoryMB = r.DefaultMemoryMB
	}
	if out.memoryMB <= 0 {
		out.memoryMB = 512
	}
	if out.cpus <= 0 {
		out.cpus = r.DefaultCPUs
	}
	if out.cpus <= 0 {
		out.cpus = 1.0
	}
	if out.pids <= 0 {
		out.pids = 256
	}
	if out.network == "" {
		out.network = gradingspec.NetworkNone
	}
	return out
}

// buildRunArgs constructs the runtime "run" arguments for one command.
func (r *ContainerRunner) buildRunArgs(image, command string, lim resolvedLimits, mountDir, name string) []string {
	mem := strconv.Itoa(lim.memoryMB) + "m"
	args := []string{
		"run", "--rm", "--name", name,
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--pids-limit", strconv.Itoa(lim.pids),
		"--memory", mem,
		"--memory-swap", mem, // equal to --memory => no swap
		"--cpus", strconv.FormatFloat(lim.cpus, 'f', -1, 64),
		"--tmpfs", "/tmp:rw,size=64m",
		"--env", "HOME=/tmp",
		"-v", mountDir + ":/work",
		"--workdir", "/work",
		"--user", r.user(),
	}
	switch lim.network {
	case gradingspec.NetworkRestricted:
		if r.RestrictedNetwork != "" {
			args = append(args, "--network", r.RestrictedNetwork)
		} else {
			args = append(args, "--network", "none") // fail safe
		}
	default:
		args = append(args, "--network", "none")
	}
	args = append(args, r.ExtraArgs...)
	args = append(args, image, "sh", "-c", command)
	return args
}

// Run executes the spec inside containers, one invocation per step.
func (r *ContainerRunner) Run(ctx context.Context, spec gradingspec.Spec, dir string) (Result, error) {
	image := spec.Image
	if image == "" {
		image = r.DefaultImage
	}
	if image == "" {
		return Result{}, errors.New("container runner: no image (set the spec's image or a default image)")
	}

	res := Result{MaxScore: spec.MaxScore()}
	cr := r.runner()
	rt := r.runtime()

	for _, step := range spec.Setup {
		out, err := r.exec1(ctx, cr, rt, image, step, r.resolveLimits(spec, nil), dir)
		if err != nil {
			return Result{}, fmt.Errorf("container runner: %w", err)
		}
		if out.timedOut || out.exitCode != 0 {
			res.Log = truncate("setup step failed: " + step + "\n" + out.stdout + out.stderr)
			for _, t := range spec.Tests {
				res.Tests = append(res.Tests, TestResult{Name: t.Name, MaxPoints: t.Points, Detail: "skipped: setup failed"})
			}
			return res, nil
		}
	}

	for _, t := range spec.Tests {
		out, err := r.exec1(ctx, cr, rt, image, t.Run, r.resolveLimits(spec, &t), dir)
		if err != nil {
			return Result{}, fmt.Errorf("container runner: %w", err)
		}

		tr := TestResult{Name: t.Name, MaxPoints: t.Points}
		passed := false
		switch {
		case out.timedOut:
			tr.Detail = "timed out"
		case t.Match != nil:
			got, exp := out.stdout, t.Match.Expected
			if t.Match.Trim {
				got, exp = strings.TrimSpace(got), strings.TrimSpace(exp)
			}
			passed = got == exp
			if !passed {
				tr.Detail = "stdout did not match expected"
			}
		default:
			passed = out.exitCode == 0
			if !passed {
				tr.Detail = "command exited non-zero"
			}
		}

		if passed {
			tr.Passed = true
			tr.Points = t.Points
			res.Score += t.Points
		} else if !out.timedOut {
			snippet := out.stdout
			if strings.TrimSpace(snippet) == "" {
				snippet = out.stderr
			}
			if snippet != "" {
				tr.Detail = tr.Detail + ": " + truncate(snippet)
			}
		}
		res.Tests = append(res.Tests, tr)
	}
	return res, nil
}

// exec1 runs a single command in a fresh container, killing a runaway container
// best-effort if the step times out (the runtime CLI dying does not stop the
// container the daemon owns).
func (r *ContainerRunner) exec1(ctx context.Context, cr commandRunner, rt, image, command string, lim resolvedLimits, dir string) (cmdResult, error) {
	name := "cairn-grade-" + id.New()
	out, err := cr.run(ctx, rt, r.buildRunArgs(image, command, lim, dir, name), lim.timeout)
	if err != nil {
		return out, err
	}
	if out.timedOut {
		// Use a fresh context so a cancelled worker context does not prevent the
		// kill from reaching the daemon (the container keeps running otherwise).
		killCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _ = cr.run(killCtx, rt, []string{"kill", name}, 10*time.Second)
		cancel()
	}
	return out, nil
}
