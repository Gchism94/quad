# CC-CA1 — Add a kernel boundary under the grading container (gVisor tier)

*Claude Code prompt. Authored in Cowork, 2026-08-08, from an isolation scan (full
scan: `outfitter-waypoint-landscape-2026-08-08.md`, Cowork project; recorded as a
platform spec at `~/dev/outfitter/specs/08-isolation-tiers.md`).

**Start from the good news: Cairn's container runner is already the strongest
sandbox in the platform.** `internal/grading/container_runner.go` sets
`--cap-drop ALL`, `--security-opt no-new-privileges`, `--read-only`,
`--pids-limit`, `--memory` with `--memory-swap` equal (no swap escape), `--cpus`,
and fails safe to `--network none`. That is a correct and well-reviewed T3 layer
and this prompt does not change it.

What is missing is **T2, the kernel boundary**. A plain OCI container shares the
host kernel and its full syscall surface, and the runtime is itself attack
surface: `runc` CVE-2024-21626 ("Leaky Vessels") resolved a container's working
directory to the host root through a leaked `/proc/self/fd` handle, and the
November 2025 trio (CVE-2025-31133, -52565, -52881) defeated masked-paths and in
some configurations AppArmor/SELinux via procfs write redirection. Hardening flags
raise the cost of an escape; they do not make one impossible.

**gVisor (`runsc`, Apache-2.0) is the right answer here on cost/benefit**: a
userspace kernel that confines the sandbox to ~53 host syscalls, roughly native
CPU performance (autograding is CPU-bound, which is the good case), tens of
milliseconds of startup, and — critically for this repo — **it is selected by a
single runtime flag on the same CLI the runner already shells out to.**

**This is a small change.** The runner already has a `Runtime` field (docker vs
podman) and an `ExtraArgs` seam. The work is a first-class, validated
`OCIRuntime`/isolation-tier option plus a `cairn doctor` check, not a rewrite.
Resist scope creep.*

---

## 1. Read first

- `internal/grading/container_runner.go` — especially `buildRunArgs` (the flag
  construction this extends), the `Runtime` field, and the `ExtraArgs` seam
- `internal/grading/container_runner_test.go` — the existing test idiom for
  asserting constructed arguments
- `~/dev/outfitter/specs/08-isolation-tiers.md` §2 (the tier table), §3 (defense
  in depth), and §7 (which explicitly records that Cairn is missing T2 only)
- `cmd/` and wherever `cairn doctor`'s container-runtime check lives — doctor
  already verifies the container runtime and does a real grading round trip, so
  this check belongs alongside those rather than as a new subsystem

## 2. Add the isolation tier as a validated option

Add an isolation-tier setting to the runner config with at least two values —
a default matching today's behaviour (`runc`, standard containers) and a
`gvisor`/`runsc` tier — rendering to `--runtime=runsc` on the run invocation.
Name it to match `specs/08`'s vocabulary (`T-standard` etc.) or the repo's own
naming register, whichever reads better in Go here; state which you chose.

**Hard requirement, from `specs/08` §5:** a requested tier is **honoured or
refused, never silently downgraded.** If the configured tier is unavailable on
this host, grading must fail loudly with an actionable message, not quietly run
with a weaker boundary. Silently satisfying a request for a stronger boundary
with a weaker one is the worst available failure mode here, and it is exactly
what an `ExtraArgs`-based workaround would produce, which is why this needs to be
a first-class validated option rather than documentation telling operators to
pass `--runtime` themselves.

Keep the default as today's behaviour so this change is non-breaking for existing
deployments; a deployment opts in.

## 3. `cairn doctor` check

Doctor already checks the container runtime and proves a grading round trip. Add:
is the configured isolation tier actually available on this host (is `runsc`
installed and registered with the daemon)? Follow doctor's existing conventions
exactly — every failure prints the fix, exit status 1 on failure. If the tier is
the default/weaker one, doctor should say so as an informational line rather than
a failure, so an operator can see their posture at a glance.

## 4. Docs

`docs/deploy.md` gets a short section: what the tiers are, why the stronger one
exists (one sentence on the shared-kernel problem, cite `specs/08`), how to
install gVisor, and the honest performance note — near-native CPU, but 2–10x on
syscall- and I/O-heavy workloads, which for a grading run that spawns many
processes or does heavy file I/O is a real cost worth measuring before mandating.

## 5. Tests

Follow `container_runner_test.go`'s existing argument-assertion idiom:

- The default tier constructs today's exact argument list (regression guard — this
  is the test that proves the change is non-breaking).
- The gVisor tier adds `--runtime=runsc` and changes nothing else.
- An unavailable configured tier produces a refusal, not a downgrade (§2's hard
  requirement — this is the important one).
- CI must not require gVisor to be installed; test the constructed arguments, not
  a live `runsc` execution.

## 6. Report

- The option's name and where it sits in config.
- Confirm the default path's arguments are byte-identical to before.
- The doctor check's output in both the available and unavailable cases.
- `go test ./...` and `go vet ./...` green.
- Any place the runner's existing structure made "refuse, never downgrade" awkward
  to enforce — flag it rather than working around it.
