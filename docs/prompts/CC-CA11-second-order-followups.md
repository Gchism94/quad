# CC-CA11 — Close the three gaps CC-CA10 found while fixing something else

*Claude Code prompt. Authored in Cowork, 2026-08-09. CC-CA10 closed three
flagged follow-ups from CC-CA1/CC-CA7, and in the process of doing that work
correctly named three more, smaller gaps in the same neighborhood — this is
the same pattern as CC-CA10 itself (a prompt surfacing real, scoped-out
issues rather than quietly working around them). None of the three below is
pilot-blocking. Same instruction as CC-CA10: treat these as independent, do
them in order, and it's fine to report on and stop after any one if a later
one turns out bigger than it looks.*

## 1. Replace the `ExtraArgs` `--runtime` special case with a security-flag deny-list

**Read first:** `internal/grading/container_runner.go`'s `validateExtraArgs`
(added in CC-CA10) and `buildRunArgs` — the full list of hardening flags it
constructs (`--cap-drop`, `--security-opt`, `--read-only`, `--pids-limit`,
`--memory`/`--memory-swap`, `--cpus`, `--user`, the network flag, plus
`--runtime`).

**The gap, in CC-CA10's own words:** `ExtraArgs` is appended last, so the
runtime CLI's last-occurrence-wins behavior means `--cap-add`,
`--privileged`, `--user`, `--network`, or `--security-opt` in `ExtraArgs` can
each undo part of the sandbox the same way `--runtime` could. `--privileged`
is the worst case — it defeats the entire T3 hardening layer in one word.

**Fix:** generalize `validateExtraArgs` from a single `--runtime` check into
a deny-list of flags that would let `ExtraArgs` override or weaken a
hardening decision `buildRunArgs` already makes. At minimum: `--runtime`
(existing), `--privileged`, `--cap-add`, `--security-opt`, `--user`,
`--network`, `--pids-limit`, `--memory`, `--memory-swap`, `--cpus`,
`--read-only` (a `--read-only=false`-shaped override, if the runtime CLI
accepts that form — check). Match both the separate-value and `--flag=value`
forms, the way the existing `--runtime` check already does. Keep the
"refuse unconditionally, even if it agrees" posture from CC-CA10 rather than
trying to compare values.

**Judgment call to make and state:** if a flag genuinely has a legitimate
use in `ExtraArgs` that doesn't weaken hardening (e.g. `--add-host`,
`--dns`, `--label`, `--tmpfs` for something beyond `/tmp`), don't deny-list
it — the goal is closing override paths for flags `buildRunArgs` already
sets for a security reason, not turning `ExtraArgs` into an allow-list of
everything. Say which flags you denied and why each one specifically
weakens or overrides existing hardening, rather than denying broadly "to be
safe."

**Test:** each denied flag, in both forms, across all three isolation
tiers; confirm a handful of legitimate `ExtraArgs` values (e.g. `--dns`,
`--label`) still validate — this must not regress
`TestExtraArgsWithoutRuntimeStillValidates` from CC-CA10.

## 2. Fix the two podman-specific gaps in `internal/doctor`

**Read first:** `internal/doctor/grading.go`'s `checkMountRoundTrip` and
`runtimeFix` — CC-CA10's report named both as working for podman "by luck"
or actively wrong.

**2a. `checkMountRoundTrip`:** confirm (don't assume) whether its
constructed flags are genuinely podman-compatible or just happen not to
have been exercised against a real podman daemon. If there's a real gap,
fix it; if it turns out to be genuinely fine, say so explicitly in a code
comment so the next person doesn't have to re-derive it, and add a test
that pins podman as a supported path here (mirroring what
`TestDockerGVisorDetectionIsUnchanged` did for the isolation check in
CC-CA10).

**2b. `runtimeFix`:** its error-string matching (`"cannot connect"`,
`"permission denied"`) and advice (`DOCKER_GID`, the daemon socket) is
Docker-specific and actively misleading for a rootless podman failure.
Branch it the same way `checkIsolationTier` now branches on
`runtime == "podman"` — give podman its own fix text (rootless podman's
actual failure modes: socket activation via `systemctl --user`, a missing
`podman machine` on non-Linux hosts, etc. — verify these against podman's
own troubleshooting docs before writing the advice, per the platform
invariant; don't guess at podman's failure surface from Docker's).

**Test:** a Docker-shaped error still gets Docker advice (regression guard);
a podman runtime with the same underlying error class gets podman-specific
advice instead.

## 3. Use a name-part's initial to break a same-name tie, instead of discarding it

**Read first:** `internal/rosteragent/rosteragent.go`'s `sameNameParts` /
`nameParts` / `normalizeName` (the normalization CC-CA10's report says drops
single-character tokens as middle initials), and `matchOne`'s new
exact-name-collection logic from CC-CA10.

**The gap:** "Jane A. Doe" and "Jane B. Doe" normalize to the same name
today, so they tie as `MatchAmbiguous` — safe, but avoidably so, since the
initial that actually distinguishes them is thrown away before the
comparison happens.

**Fix:** when normalization would otherwise produce a tie, use any
retained initials as an additional tiebreaker before falling back to
`MatchAmbiguous` — a middle initial is a weak signal on its own (don't use
it to promote a match to `MatchExact`), but it's a legitimate way to narrow
a tie down to a single `MatchNeedsConfirm` candidate instead of leaving two
tied options an instructor has to manually distinguish. If a middle initial
narrows the field to exactly one candidate, that candidate becomes a
`MatchNeedsConfirm` (not `MatchExact` — the instructor still confirms,
since an initial alone isn't as strong as a full-name match). If initials
don't fully resolve the tie (e.g. both LMS and roster data lack them, or
more than one candidate shares the same initial), it stays
`MatchAmbiguous` exactly as before.

**Test:** "Jane A. Doe" vs. two candidates "Jane A. Doe" / "Jane B. Doe"
resolves to `MatchNeedsConfirm` on the correct one; the existing
same-name-tie tests (no initials available) still produce `MatchAmbiguous`
unchanged; a case where both candidates share the same initial still ties.

## 4. Report

For each of the three, independently: what changed, the new/changed
test(s) confirmed passing, and — same as CC-CA10 — name (don't
necessarily fix) any further instance of the same class of gap you notice
while in that code.

Overall: `go test ./... -race` and `go vet ./...` green; confirm
CC-CA10's own tests (`TestExtraArgsMayNotSelectTheRuntime`,
`TestDockerGVisorDetectionIsUnchanged`, the same-name-collision suite)
still pass unchanged where this prompt didn't intend to touch their
behavior.
