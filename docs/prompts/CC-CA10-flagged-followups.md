# CC-CA10 — Close three follow-ups flagged by their own execution reports

*Claude Code prompt. Authored in Cowork, 2026-08-09. Three separate prompts
(CC-CA1, CC-CA7) each shipped correctly and each explicitly named a real gap
as out of scope for that prompt rather than quietly working around it. None
is pilot-blocking on its own, but they're small, well-understood, and cheap
to close now rather than letting three "worth a follow-up" notes accumulate
silently. Treat these as three independent fixes — do them in the order
below, and it is fine to report on and stop after any one of them if a later
one turns out to be bigger than it looks; say so rather than forcing all
three into one commit.*

## 1. Close the `ExtraArgs` isolation-tier escape hatch (from CC-CA1)

**Read first:** `internal/grading/container_runner.go` — `ContainerRunner`'s
`ExtraArgs` field and where it's appended in `buildRunArgs`; the
`IsolationTier` type and `resolve()`/`ValidateIsolation()` from CC-CA1.

**The gap:** `CAIRN_GRADER_ISOLATION` is validated and refused-not-downgraded,
but nothing stops an operator's `ExtraArgs` from containing its own
`--runtime` flag — either duplicating the tier's choice harmlessly, or
smuggling in a *weaker* runtime than the configured tier asked for, silently
defeating the whole point of CC-CA1.

**Fix:** reject a `--runtime` (or `--runtime=...`) entry in `ExtraArgs` at
the same validation point `ValidateIsolation()` already runs at (startup, in
`main.go`, not per-run) — fail loudly with a message naming the conflict,
same posture as an unknown isolation tier. Do not try to merge or reconcile
the two; refusing is consistent with CC-CA1's own hard requirement.

**Test:** `ExtraArgs` containing `--runtime` fails validation regardless of
which isolation tier is configured, including when `ExtraArgs`'s runtime
would coincidentally match the configured tier — the rule is "don't allow
it," not "don't allow it if it disagrees."

## 2. Fix (or explicitly scope down) gVisor detection under podman (from CC-CA1)

**Read first:** `internal/doctor/grading.go`'s `checkIsolationTier` — it
queries `<runtime> info --format '{{.Runtimes}}'`, which is Docker's output
shape. Check what `podman info` actually exposes for registered OCI
runtimes (this may require reading podman's own docs/`info` schema rather
than assuming — verify before writing the fix, per CLAUDE.md's platform
invariant about not assuming an external system's behavior).

**Two acceptable outcomes**, pick whichever is true once you've checked:
- If podman has an equivalent way to ask "is runsc registered," branch on
  `runtime == "podman"` and query it correctly.
- If podman genuinely has no daemon-side registered-runtimes concept (i.e.
  the runtime is chosen per-invocation rather than pre-registered), say so
  explicitly in the doctor check's message for the podman case — rather than
  running a Docker-shaped query that happens to fail closed by accident,
  make the failure-closed behavior *intentional and stated*, e.g. "podman
  gVisor detection is not implemented; verify runsc manually" rather than a
  generic "runsc not registered."

**Test:** cover whichever branch you land on; if podman's case becomes an
explicit "not implemented, verify manually" message, test that the message
says so rather than misleadingly claiming runsc was checked.

## 3. Give the same-name collision in `MatchRoster` a real resolution path (from CC-CA7)

**Read first:** `internal/rosteragent/rosteragent.go`'s `MatchRoster`/
`matchOne`, and `TestMatchAmbiguousIsUnmatched` in `rosteragent_test.go` —
today, two candidates with the same full name correctly fall to `MatchNone`
rather than guessing, but the instructor has no path to resolve it other
than falling all the way back to CC-CA6's manual bulk-add for that one
student.

**Fix:** when `MatchRoster` produces an ambiguous match (multiple candidates
tie on name), surface the tied candidates as part of the `Match`'s data
(reuse or extend the existing `Candidate` list already used for
`MatchNeedsConfirm`) so `cmd/cairn/roster_pull.go`'s `reviewMatches` can list
the *specific* ambiguous candidates to the instructor and let them pick one
interactively — the same interaction shape already built for
`MatchNeedsConfirm`, not a new UI concept. If a row's ambiguity can't be
resolved interactively in this CLI flow for some reason, it must still list
as skipped with the reason named (never silently dropped) — check
`BuildPayload`'s skipped-row reporting already does this for `MatchNone` and
extend it rather than replacing it.

**Test:** two candidates with an identical name produce a match the reviewer
can disambiguate (not just detect); the resulting selection round-trips
correctly into `BuildPayload`'s output for whichever candidate was chosen.

## 4. Report

For each of the three, independently:
- What changed and where.
- The new/changed test(s), confirmed passing.
- Whether you found any other instance of the same class of gap while in
  that code (e.g., another place `ExtraArgs` could smuggle in something,
  another runtime-shape assumption) — name it even if you don't fix it in
  this pass.

Overall: `go test ./... -race` and `go vet ./...` still green; confirm the
gVisor availability check for the *Docker* case (CC-CA1's original,
already-tested path) still behaves identically — this prompt should refine
podman's case, not regress Docker's.
