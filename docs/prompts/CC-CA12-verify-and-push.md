# CC-CA12 — Real verification of CA1/CA10/CA11, then push and watch CI's first live run

*Claude Code prompt. Authored in Cowork, 2026-08-09. Every commit since
CC-CA1 was reviewed and landed from execution reports plus a full manual
line-by-line code read in Cowork — but Cowork's own sandbox has a network
policy that blocks `proxy.golang.org` entirely (both the Go toolchain and
module downloads), so `go build`/`go vet`/`go test` could not be
independently re-run there for CC-CA1, CC-CA10, or CC-CA11. This prompt is
that missing real run, from an environment that actually has the toolchain
and network access — plus the two things Cowork categorically cannot do:
push to GitHub, and watch CC-CA9's workflow execute on a real hosted
runner for the first time ever.*

## 1. Full local verification

Run each of these for real and report the actual output, not a summary:

- `go build ./...`
- `go vet ./...`
- `gofmt -l .` (must print nothing)
- `go test ./... -race -count=1` — all packages, paying particular
  attention to the newest test files since they're the ones never verified
  outside an execution report's own claims:
  `internal/grading/isolation_test.go`, `internal/doctor/isolation_test.go`,
  `internal/doctor/podman_test.go`, `internal/rosteragent/rosteragent.go`'s
  test file.
- `cd web && npm ci && npm run typecheck && npm test && npm run build`

If anything genuinely fails, fix it and say exactly what was wrong — don't
silently patch around a real bug without naming it, and don't reflexively
distrust something that turns out to already be correct. This is a
verification pass, not a rewrite.

## 2. Push

`main` is currently 25 commits ahead of `origin/main` with nothing pushed —
this whole backlog (CC-CA1, CC-CA3, CC-CA6 through CC-CA11, plus the
ROADMAP/registry housekeeping commits) exists only locally right now.
Once step 1 is clean, `git push origin main`.

## 3. Watch CI's actual first run

CC-CA9's `.github/workflows/ci.yml` has never executed on a real GitHub
Actions runner — every claim about it passing so far came from replaying
its `run:` commands locally, which cannot prove action resolution, cache
behavior, or the hosted Ubuntu image actually work. After pushing, watch
the workflow run triggered by that push (`gh run watch` or the Actions
tab) to completion.

If it fails for a reason a local run couldn't have caught (action
resolution, a runner-image difference, a cache issue), fix it and note
specifically what local verification missed. If it passes clean, that's
the first real proof CC-CA9 asked for and didn't have yet.

## 4. Report

- Full output (not paraphrased) for every command in step 1 — pass or
  fail.
- Anything that was actually broken, what it was, and the fix. If nothing
  was broken, say so plainly rather than implying something was found.
- The pushed commit hash and confirmation `origin/main` now matches local
  `main`.
- The CI run's URL/ID and outcome, and whether branch-protection should
  now be turned on for `main` given it's gone green for real (CC-CA9's own
  report recommended waiting for a few real green runs before requiring
  it — this is the first one).
