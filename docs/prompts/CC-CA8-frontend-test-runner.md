# CC-CA8 — Stand up a frontend test runner: this has now cost two prompts a real test

*Claude Code prompt. Authored in Cowork, 2026-08-09. `web/package.json` has no
test runner at all — no vitest, no jest, no test files (confirmed:
`dev`/`build`/`preview`/`typecheck` are the only scripts). This has already
cost two separate prompts their intended frontend test:

- **CC-CA3** couldn't write the requested unit test for the invite-link URL
  builder; verified instead by checking the built bundle and matching the
  generated URL shape against the server's route by hand.
- **CC-CA6** couldn't write the requested tests for `roster-parse.ts`'s CSV/
  paste parsing and hashing; verified instead by compiling with `tsc` and
  running 15 behavioral checks against the emitted JS directly — real
  verification, but it evaporated with that session and isn't checked in.

`roster-parse.ts` in particular is exactly the kind of logic that regresses
silently — CSV quoting, header-row detection, hex-digest detection, the
privacy-critical "plaintext email never reaches the payload" property — and
right now nothing catches a regression in it except another one-off manual
check next time someone happens to touch that file.*

## 1. Read first

- `web/package.json` — the existing scripts to extend, not replace
- `web/src/roster-parse.ts` — the highest-value first test target: parsing
  (one-per-line, CSV, CRLF, header rows, quoted fields, extra LMS columns)
  and hashing (salted per-classroom, secure-context fallback, plaintext
  never in the output)
- `web/src/api.ts`'s `inviteUrl` — the second target, from CC-CA3
- Cairn's Go side already has a real test culture (`go test ./... -race`);
  match that seriousness on the frontend, don't add a token single test

## 2. Pick and wire up a runner

Recommend **Vitest** — same author/ecosystem as Vite (already the build
tool here), so config overlap is minimal and it's the de facto standard for
Vite projects. Add it as a dev dependency, add a `test` script to
`package.json`, and wire a minimal `vitest.config.ts` if the existing
`vite.config.ts` doesn't already cover it. Don't add a component-testing
library (Testing Library, etc.) as part of this prompt unless a test
genuinely needs to render a component — `roster-parse.ts` and `api.ts`'s
URL builders are pure functions and don't need one.

## 3. Backfill the two tests that already should have existed

- Port CC-CA6's 15 behavioral checks on `roster-parse.ts` into real
  `roster-parse.test.ts` cases: one-per-line, CSV, CRLF line endings, a
  header row, quoted fields, extra trailing columns, a hex-digest second
  column vs. a plaintext-email second column, and — explicitly — a test
  asserting the plaintext address never appears anywhere in `toBulkEntries`'s
  output.
- Port CC-CA3's invite-URL check into a real test: `api.inviteUrl(id)`
  produces `${origin}${BASE}/assignments/${id}/accept` for a given
  assignment ID.

## 4. Wire into the build/CI path

Add `test` (or extend `build`) so a broken frontend test actually fails
something a contributor or CI would notice — check whether Cairn has CI
config in-repo (`.github/workflows/`) and add the frontend test step there
if so; if there's no CI config yet, say so plainly rather than assuming.

## 5. Report

- Which runner was chosen and the exact `package.json` script added.
- The two backfilled test files, and confirm both pass.
- Whether CI already runs frontend checks and, if so, that the new test
  step was added there too.
- `go test ./...` and the frontend build both still green (this prompt
  should not touch Go code at all).
