# CC-CA9 — Stand up CI: nothing runs the tests but a human remembering to

*Claude Code prompt. Authored in Cowork, 2026-08-09. `.github/` currently
contains only `ISSUE_TEMPLATE/` and `pull_request_template.md` — no
`.github/workflows/`, and no GitLab CI, Travis, Jenkins, CircleCI, or Azure
Pipelines config either (checked during CC-CA8). This repo now has two real
test suites — `go test ./...` (Go, existing) and `npm test` via Vitest
(frontend, added in CC-CA8) — and nothing runs either one except a
contributor remembering to, which is exactly the gap CC-CA8's own report
named as a follow-up worth closing.*

## 1. Read first

- `.github/pull_request_template.md` — the existing contributor-facing
  surface this should sit alongside
- `web/README.md`'s new "Test" section (from CC-CA8) — what `npm test` runs
  and how it's invoked
- `web/package.json` — Node/npm version implied by its dependencies
  (Vite 7, Vitest 4)
- `go.mod` — the Go version this repo requires (`go 1.25.0`); the workflow's
  `go-version` must match or CI fails on a toolchain mismatch a contributor
  can't fix
- Whether a `Dockerfile`/`compose.yaml`-based build step already exists that
  a CI job should mirror rather than duplicate differently

## 2. Add a single CI workflow

Add `.github/workflows/ci.yml` (or split Go/frontend into two files if that
reads cleaner — say which and why). At minimum, on push and pull_request:

- **Go job**: checkout, set up Go at the exact version from `go.mod`,
  `go build ./...`, `go vet ./...`, `go test ./... -race`. Match the
  seriousness already implied by `-race` being the norm in this repo — don't
  quietly drop it for CI speed without saying so.
- **Frontend job**: checkout, set up Node (pick a version compatible with
  Vite 7/Vitest 4 — state which and why), `npm ci` (not `npm install`, for a
  reproducible CI install from the committed lockfile), `npm run typecheck`,
  `npm test`, and `npm run build` (catches a build break even though tests
  and build are deliberately separate locally per CC-CA8).
- Cache Go modules and npm dependencies so CI doesn't refetch the world every
  run — use the standard `actions/setup-go`/`actions/setup-node` cache
  options rather than a hand-rolled cache step.
- Fail the job (non-zero exit) on any of the above failing — no
  `continue-on-error`, no swallowed exit codes.

## 3. Don't scope-creep

This prompt is CI only. Explicitly out of scope:

- No deployment/release workflow.
- No gVisor-in-CI: CC-CA1's tests assert constructed arguments, not a live
  `runsc` execution, specifically so CI doesn't need it installed — keep it
  that way.
- No branch-protection rule changes (that's a GitHub repo setting, not
  something a workflow file controls) — note in your report whether you'd
  recommend requiring this check on `main`, but don't attempt to configure
  it.

## 4. Report

- The exact workflow file(s) added, and the Go/Node versions pinned and why.
- Confirm a deliberately-broken Go test and a deliberately-broken frontend
  test each fail the workflow (describe how you verified this — a real push
  to a scratch branch/PR if you have the access to do so cheaply, or a
  careful dry-run/local `act`-style reasoning if not; say which).
- Whether you'd recommend making this check required for merges to `main`,
  and why, without changing the setting yourself.
- `go test ./...` and `npm test` both still green on `main` as of this
  change (this prompt should add automation, not touch application code).
