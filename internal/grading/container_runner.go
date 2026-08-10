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
	Runtime           string        // "docker" (default) or "podman"
	Isolation         IsolationTier // "" / "shared" (default) or "gvisor"; see IsolationTier
	DefaultImage      string        // used when a spec sets no image
	RestrictedNetwork string        // runtime network name for NetworkRestricted
	User              string        // --user value; default is the server's own uid:gid (set CAIRN_GRADER_USER to override)
	ExtraArgs         []string      // additional runtime args (advanced)

	DefaultTimeout  time.Duration // per-step fallback; default 30s
	DefaultMemoryMB int           // default 512
	DefaultCPUs     float64       // default 1.0
	DefaultPids     int           // default 256

	exec commandRunner // injected in tests; nil -> real exec
}

// IsolationTier selects the kernel boundary under the container. The hardening
// flags above (T3) are applied identically at every tier; this chooses what sits
// underneath them (T2).
//
// The names are Cairn's own rather than the platform spec's `T-standard`/
// `T-strong` labels. The spec calls gVisor "T-standard" because it is the
// platform's *recommended* default — but it is not Cairn's default, and reusing
// the label here would tell an operator reading this struct that they already
// have a kernel boundary when they do not. The mapping is recorded in
// docs/deploy.md.
type IsolationTier string

const (
	// IsolationShared is today's behaviour and the default: an ordinary OCI
	// container sharing the host kernel. Strong T3 hardening, no T2 boundary —
	// a kernel or runtime vulnerability (runc CVE-2024-21626 and the November
	// 2025 procfs trio, for example) is reachable from student code.
	IsolationShared IsolationTier = "shared"

	// IsolationGVisor runs the container under gVisor (runsc), a userspace
	// kernel that confines the sandbox to roughly 53 host syscalls. Opt-in:
	// runsc must be installed and registered with the runtime daemon.
	IsolationGVisor IsolationTier = "gvisor"
)

// gvisorRuntimeName is the OCI runtime `runsc` registers itself as.
const gvisorRuntimeName = "runsc"

// resolve returns the tier to use, or an error for an unrecognised value.
//
// An unknown tier is an error rather than a fallback to the default. That is the
// whole point of this type: a deployment that asks for a boundary it does not get
// is worse off than one that never asked, because it believes it is protected.
func (t IsolationTier) resolve() (IsolationTier, error) {
	switch t {
	case "", IsolationShared:
		return IsolationShared, nil
	case IsolationGVisor:
		return IsolationGVisor, nil
	default:
		return "", fmt.Errorf(
			"unknown isolation tier %q: valid values are %q (default, shared host kernel) and %q (gVisor/runsc kernel boundary)",
			string(t), string(IsolationShared), string(IsolationGVisor))
	}
}

// ValidateIsolation reports whether the configured tier is a recognised value
// and whether ExtraArgs conflicts with it. Callers that construct a runner from
// configuration should call this at startup so a misconfiguration fails
// immediately rather than at the first grading run.
func (r *ContainerRunner) ValidateIsolation() error {
	if _, err := r.Isolation.resolve(); err != nil {
		return err
	}
	return r.validateExtraArgs()
}

// deniedExtraArg lists the flags ExtraArgs may not contain, each with the
// hardening decision it would undo.
//
// Every entry here is a flag buildRunArgs already sets for a security reason.
// ExtraArgs is appended last, and the runtime CLIs take the final occurrence of
// a repeated flag, so an entry here does not add to the sandbox — it replaces
// part of it. Flags with a legitimate non-hardening use (--dns, --label,
// --add-host, --env, extra --tmpfs mounts, --volume for read-only reference
// data) are deliberately absent: the goal is closing override paths, not
// turning ExtraArgs into an allow-list.
//
// The override behaviour of each was confirmed against a real Docker daemon
// rather than assumed — see the notes below.
var deniedExtraArgs = map[string]string{
	// Verified: `--cap-drop ALL --cap-add CHOWN` lets chown succeed inside the
	// container, so a later --cap-add re-grants what --cap-drop ALL removed.
	"--cap-add": "re-grants capabilities that --cap-drop ALL removes",
	// Not repeat-tested because it needs no repeat: --privileged turns off the
	// whole confinement layer (capabilities, device access, seccomp/AppArmor) in
	// one word, whatever else is on the command line.
	"--privileged": "disables the entire confinement layer at once",
	// Verified: `--user 65534:65534 --user 0:0` reports uid 0. The last --user
	// wins, so this is a direct path from the unprivileged grading user to root
	// inside the container.
	"--user": "overrides the unprivileged --user the runner sets",
	// Verified: repeating --network with "none" errors out rather than silently
	// downgrading — but that breaks every grading run, and any value here
	// contradicts the egress policy the grading spec declares.
	"--network": "contradicts the spec-declared egress policy (and errors when repeated with none)",
	// Verified: `--memory 512m --memory 1g` reports a 1 GiB cgroup limit.
	"--memory": "raises the memory cap the grading spec set",
	// Same last-wins path, and raising it alone re-enables swap escape, since
	// the runner's protection is --memory-swap being equal to --memory.
	"--memory-swap": "re-enables swap escape by unpinning --memory-swap from --memory",
	// Verified: `--pids-limit 256 --pids-limit 4096` reports 4096.
	"--pids-limit": "raises the process cap that bounds fork bombs",
	// Verified: `--cpus 1 --cpus 2` reports a 2-CPU quota.
	"--cpus": "raises the CPU quota the grading spec set",
	// --security-opt accumulates rather than overrides (NoNewPrivs stayed set in
	// testing), but an added value such as seccomp=unconfined or
	// apparmor=unconfined still removes a layer the runner relies on.
	"--security-opt": "can add seccomp=unconfined or apparmor=unconfined alongside no-new-privileges",
	// Verified: `--read-only=false` is accepted by Docker, so the boolean's
	// negated form makes the root filesystem writable again.
	"--read-only": "makes the root filesystem writable again via --read-only=false",
	// The OCI runtime is the isolation tier's decision; see IsolationTier.
	"--runtime": "overrides the OCI runtime chosen by the isolation tier",
}

// validateExtraArgs refuses an ExtraArgs entry that would override a hardening
// decision buildRunArgs already makes.
//
// This refuses unconditionally rather than comparing values. "--runtime=runsc
// alongside Isolation=gvisor" happens to be harmless today, but permitting it
// makes each setting a two-source value, and the next edit to either source
// silently decides which one wins. One setting, one source.
func (r *ContainerRunner) validateExtraArgs() error {
	for _, a := range r.ExtraArgs {
		flag := a
		if i := strings.IndexByte(a, '='); i >= 0 {
			flag = a[:i] // --flag=value
		}
		reason, denied := deniedExtraArgs[flag]
		if !denied {
			continue
		}
		if flag == "--runtime" {
			return fmt.Errorf(
				"ExtraArgs may not contain %q: %s (currently %q). Remove it from ExtraArgs and set "+
					"the tier instead — valid tiers are %q and %q",
				a, reason, string(r.EffectiveIsolation()), string(IsolationShared), string(IsolationGVisor))
		}
		return fmt.Errorf(
			"ExtraArgs may not contain %q: it %s. ExtraArgs is appended after the runner's own "+
				"flags, so this would weaken the sandbox rather than add to it — set the limit in the "+
				"grading spec if it needs to change",
			a, reason)
	}
	return nil
}

// EffectiveIsolation is the tier this runner will actually use, with the empty
// value resolved to the default. It reports IsolationShared for an invalid
// value; call ValidateIsolation to detect that case rather than inferring it
// from this result.
func (r *ContainerRunner) EffectiveIsolation() IsolationTier {
	tier, err := r.Isolation.resolve()
	if err != nil {
		return IsolationShared
	}
	return tier
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
//
// tier must already be resolved (see IsolationTier.resolve); passing an
// unvalidated value here would be the silent-downgrade bug this design exists to
// prevent, so Run resolves once up front and fails before reaching this point.
func (r *ContainerRunner) buildRunArgs(image, command string, lim resolvedLimits, mountDir, name string, tier IsolationTier) []string {
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
	// Appended only for a non-default tier, so the default path's argument list
	// stays byte-identical to what it was before isolation tiers existed.
	if tier == IsolationGVisor {
		args = append(args, "--runtime", gvisorRuntimeName)
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

	// Resolve the isolation tier before running anything. A misconfigured tier
	// must stop the run here: grading student code under a weaker boundary than
	// the operator asked for is a worse outcome than not grading at all.
	tier, err := r.Isolation.resolve()
	if err != nil {
		return Result{}, fmt.Errorf("container runner: %w", err)
	}

	res := Result{MaxScore: spec.MaxScore()}
	cr := r.runner()
	rt := r.runtime()

	for _, step := range spec.Setup {
		out, err := r.exec1(ctx, cr, rt, image, step, r.resolveLimits(spec, nil), dir, tier)
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
		out, err := r.exec1(ctx, cr, rt, image, t.Run, r.resolveLimits(spec, &t), dir, tier)
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
func (r *ContainerRunner) exec1(ctx context.Context, cr commandRunner, rt, image, command string, lim resolvedLimits, dir string, tier IsolationTier) (cmdResult, error) {
	name := "cairn-grade-" + id.New()
	out, err := cr.run(ctx, rt, r.buildRunArgs(image, command, lim, dir, name, tier), lim.timeout)
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
