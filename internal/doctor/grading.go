// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"context"
	"strings"
)

// Grading modes, mirroring CAIRN_GRADER in cmd/cairn.
const (
	GraderContainer = "container"
	GraderLocalExec = "local-exec-unsafe"
)

// GradingConfig is the grading setup to diagnose.
type GradingConfig struct {
	// Mode is CAIRN_GRADER: "", "container", or "local-exec-unsafe".
	Mode string
	// Runtime is the resolved container runtime binary ("docker" or "podman").
	Runtime string
	// Isolation is CAIRN_GRADER_ISOLATION: "", "shared", or "gvisor".
	Isolation string
	// Image is CAIRN_GRADER_IMAGE; may be empty if every spec sets its own.
	Image string
	// WorkDir is where grading checkouts are created (the effective TMPDIR).
	// Empty means the OS default.
	WorkDir string
}

// Commander runs an external command and returns its combined output.
type Commander interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// ProbeFS creates the scratch directory used by the mount round-trip.
type ProbeFS interface {
	MkdirTemp(dir, pattern string) (string, error)
	WriteFile(path string, data []byte) error
	RemoveAll(path string) error
}

// probeSentinel is written host-side and read container-side.
const probeSentinel = "cairn-doctor-mount-probe"

// CheckGrading diagnoses the grading runner. Its most important job is the mount
// round-trip: grading bind-mounts a host directory into each student container,
// so when Cairn itself runs in a container talking to the host's Docker daemon,
// that directory must exist at the SAME path on the host. Otherwise every
// grading run fails with an empty or missing checkout, which is close to
// undiagnosable from the outside.
func CheckGrading(ctx context.Context, cfg GradingConfig, cmd Commander, fsys ProbeFS) []Result {
	const name = "grading"

	switch cfg.Mode {
	case "":
		return []Result{warnf(name,
			"set CAIRN_GRADER=container to enable sandboxed autograding",
			"disabled — grade requests will be rejected")}
	case GraderLocalExec:
		return []Result{warnf(name,
			"use CAIRN_GRADER=container for anything running student-submitted code",
			"local-exec runner — student code runs with NO sandbox on this host")}
	case GraderContainer:
		// fall through
	default:
		return []Result{failf(name,
			"CAIRN_GRADER must be \"container\" or \"local-exec-unsafe\"",
			"unknown CAIRN_GRADER value %q", cfg.Mode)}
	}

	var out []Result
	runtime := cfg.Runtime
	if runtime == "" {
		runtime = "docker"
	}

	version, err := cmd.Run(ctx, runtime, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return append(out, failf(name, runtimeFix(runtime, version, err),
			"%s is not usable: %v", runtime, err))
	}
	out = append(out, okf(name, "%s server %s", runtime, strings.TrimSpace(version)))
	out = append(out, checkIsolationTier(ctx, cfg, runtime, cmd))

	if cfg.Image == "" {
		out = append(out, warnf(name,
			"set CAIRN_GRADER_IMAGE to a default image, or make sure every grading spec names its own",
			"no default image — grading fails for any spec that does not set one"))
		return out // nothing to probe the mount with
	}

	out = append(out, checkMountRoundTrip(ctx, cfg, runtime, cmd, fsys))
	return out
}

// Isolation tiers, mirroring CAIRN_GRADER_ISOLATION in internal/grading.
const (
	IsolationShared = "shared"
	IsolationGVisor = "gvisor"
)

// checkIsolationTier reports the kernel boundary under grading containers.
//
// The default tier is an informational line, not a warning: sharing the host
// kernel is the documented default and a deployment that has not opted in is not
// misconfigured. A *requested* tier that is unavailable is a failure, because
// grading would otherwise run with a weaker boundary than the operator asked for.
func checkIsolationTier(ctx context.Context, cfg GradingConfig, runtime string, cmd Commander) Result {
	const name = "grading isolation"

	switch cfg.Isolation {
	case "", IsolationShared:
		return okf(name,
			"shared host kernel (default) — hardened container, no kernel boundary; "+
				"set CAIRN_GRADER_ISOLATION=gvisor to add one (see docs/deploy.md)")

	case IsolationGVisor:
		// The registered-runtimes question only has a daemon-side answer under
		// Docker. Podman is daemonless: runtimes are declared per-user in
		// containers.conf and chosen per-invocation, and `podman info` exposes
		// only .Host.ociRuntime — the runtime *currently in use* — with no list
		// of the alternatives available. Verified against podman's own info(1)
		// documentation, 2026-08-09.
		//
		// Running Docker's query against podman therefore returns an empty
		// string and would report "runsc not registered" for a perfectly working
		// gVisor setup. Saying "not checked" is the honest result; claiming a
		// check that did not happen is worse than declining to check.
		if runtime == "podman" {
			return warnf(name, podmanGVisorFix(),
				"CAIRN_GRADER_ISOLATION=gvisor, but this check is not implemented for podman — "+
					"podman is daemonless and has no registered-runtimes list to query, so gVisor "+
					"availability was NOT verified here. Confirm it manually")
		}

		// Ask the daemon what runtimes it has registered. Checking for the runsc
		// binary on PATH would not be enough: Cairn may run in a container while
		// the daemon lives on the host, so what matters is what the *daemon* can
		// select, not what this filesystem happens to contain.
		out, err := cmd.Run(ctx, runtime, "info", "--format", "{{.Runtimes}}")
		if err != nil {
			return failf(name, gvisorFix(runtime),
				"cannot ask %s which runtimes it has registered: %v", runtime, err)
		}
		if !strings.Contains(out, "runsc") {
			return failf(name, gvisorFix(runtime),
				"CAIRN_GRADER_ISOLATION=gvisor but %s has no \"runsc\" runtime registered — "+
					"grading would refuse to run rather than fall back to a weaker boundary",
				runtime)
		}
		return okf(name, "gVisor (runsc) registered with %s — grading runs behind a userspace kernel", runtime)

	default:
		return failf(name,
			"set CAIRN_GRADER_ISOLATION to \""+IsolationShared+"\" or \""+IsolationGVisor+"\", or unset it for the default",
			"unknown CAIRN_GRADER_ISOLATION value %q", cfg.Isolation)
	}
}

// podmanGVisorFix tells the operator how to confirm what doctor could not.
// Podman selects the runtime per invocation, which is exactly how Cairn's
// grading runner passes it — so a correct setup here really is unverifiable
// from a daemon query, not merely unimplemented out of laziness.
func podmanGVisorFix() string {
	return "verify gVisor manually — podman has no daemon to ask, so confirm both:\n" +
		"  runsc --version                        # the binary is installed\n" +
		"  podman run --rm --runtime runsc alpine true   # podman can actually use it\n" +
		"declare it in containers.conf ([engine] runtimes) if that fails:\n" +
		"  https://gvisor.dev/docs/user_guide/quick_start/podman/\n" +
		"grading passes --runtime runsc per container, so a working command above\n" +
		"means grading will work; this check simply cannot confirm it for you"
}

func gvisorFix(runtime string) string {
	return "install gVisor and register it with " + runtime + ", or unset CAIRN_GRADER_ISOLATION to\n" +
		"accept the shared-kernel default:\n" +
		"  https://gvisor.dev/docs/user_guide/install/\n" +
		"  sudo " + runtime + " info --format '{{.Runtimes}}'   # confirm runsc appears\n" +
		"on Docker, `runsc install` writes the runtime into /etc/docker/daemon.json;\n" +
		"restart the daemon afterwards (sudo systemctl restart docker)"
}

// runtimeFix turns the runtime's own error into the specific thing to change.
func runtimeFix(runtime, output string, err error) string {
	s := strings.ToLower(output + " " + err.Error())
	switch {
	case strings.Contains(s, "permission denied"):
		return "the container cannot use the Docker socket.\n" +
			"on the host run: getent group docker | cut -d: -f3\n" +
			"then set DOCKER_GID to that number in .env and re-run `docker compose up -d`"
	case strings.Contains(s, "cannot connect") || strings.Contains(s, "no such file"):
		return "the Docker daemon is not reachable.\n" +
			"on a host install: start it with `sudo systemctl enable --now docker`\n" +
			"in a container: mount the socket (-v /var/run/docker.sock:/var/run/docker.sock)"
	case strings.Contains(s, "executable file not found") || strings.Contains(s, "not found"):
		return "install " + runtime + ", or set CAIRN_GRADER_RUNTIME to one that is installed\n" +
			"(the Cairn image ships the docker CLI; a host install needs docker.io)"
	default:
		return "confirm " + runtime + " works for this user: `" + runtime + " version`"
	}
}

// checkMountRoundTrip writes a sentinel file on the host side and reads it back
// from inside a container, mounted exactly the way the grading runner mounts a
// checkout.
func checkMountRoundTrip(ctx context.Context, cfg GradingConfig, runtime string, cmd Commander, fsys ProbeFS) Result {
	const name = "grading workdir"

	dir, err := fsys.MkdirTemp(cfg.WorkDir, "cairn-doctor-*")
	if err != nil {
		return failf(name,
			"make sure the work directory exists and is writable by the Cairn user;\n"+
				"in Docker it is the TMPDIR value and must be a bind mount",
			"cannot create a scratch directory in %s: %v", workDirLabel(cfg.WorkDir), err)
	}
	defer func() { _ = fsys.RemoveAll(dir) }()

	if err := fsys.WriteFile(dir+"/probe.txt", []byte(probeSentinel)); err != nil {
		return failf(name, "make the work directory writable by the Cairn user",
			"cannot write into %s: %v", dir, err)
	}

	// Mirrors internal/grading's mount: -v <hostdir>:/work.
	got, err := cmd.Run(ctx, runtime, "run", "--rm", "--network", "none",
		"-v", dir+":/work", cfg.Image, "sh", "-c", "cat /work/probe.txt")
	if err != nil || !strings.Contains(got, probeSentinel) {
		return failf(name, mountFix(cfg.WorkDir),
			"a directory Cairn created (%s) is not visible inside a grading container — "+
				"every grading run would find an empty checkout", dir)
	}
	return okf(name, "%s is visible to %s at the same path", workDirLabel(cfg.WorkDir), runtime)
}

func mountFix(workDir string) string {
	if workDir == "" {
		return "set TMPDIR to a directory that is bind-mounted at the SAME path on host and container,\n" +
			"e.g. TMPDIR=/var/lib/cairn/work with -v /var/lib/cairn/work:/var/lib/cairn/work.\n" +
			"the Docker daemon resolves -v paths on the HOST, so a container-only path is empty"
	}
	return "bind-mount " + workDir + " at the same path on both sides:\n" +
		"  -v " + workDir + ":" + workDir + "\n" +
		"the Docker daemon resolves -v paths on the HOST, so a container-only path is empty"
}

func workDirLabel(dir string) string {
	if dir == "" {
		return "the default temp directory"
	}
	return dir
}
