# CC-CA14 — Fold accented/Unicode names in the roster matcher

*Claude Code prompt. Authored 2026-08-10, from a user-supplied commit-message
draft describing the same gap CC-CA13 already scoped and wrote up:
`normalizeName` in `internal/rosteragent/rosteragent.go` lowercases and
collapses whitespace only, so an accented LMS name (e.g. "José") never
matches an unaccented Git-host profile name ("Jose") at any tier — exact,
`sameNameParts`, or the CC-CA11 initial-narrowing. CC-CA11's own execution
report flagged this as the more likely real-world gap versus the
middle-initial case it fixed.*

**Before executing this prompt, read `docs/prompts/CC-CA13-unicode-name-matching.md`
first** — it already covers this exact gap in full (the fix design, the
`golang.org/x/text` dependency-promotion note, the precomposed-vs-decomposed
test case, and the `squash()` scoping decision). Confirm against
`internal/rosteragent/rosteragent.go` and `rosteragent_test.go` whether
CC-CA13 has already been implemented:

- If `normalizeName` already strips diacritics (imports
  `golang.org/x/text/unicode/norm` et al.) and the table-driven tests below
  are already present and passing, **this prompt is redundant — stop, report
  that CC-CA13 is already implemented, and do not make further changes.**
- If CC-CA13 was only written as a spec and never executed, treat this
  prompt as the execution pass for that spec (follow CC-CA13's §2–§5
  verbatim) rather than re-deriving the design independently.

## 1. Read first

- `docs/prompts/CC-CA13-unicode-name-matching.md` (the existing spec for
  this exact fix)
- `internal/rosteragent/rosteragent.go` — `normalizeName`, `matchOne`,
  `sameNameParts`, `initialsOf`, `squash`
- `internal/rosteragent/rosteragent_test.go` — existing CC-CA10/CC-CA11
  table-driven cases, so additions extend rather than duplicate them
- `go.mod` — confirm current status of `golang.org/x/text` (indirect vs.
  direct)

## 2. If not yet implemented: apply the fix

Per CC-CA13 §2: fold diacritics out during normalization only — comparison
becomes accent-insensitive, but no display/audit string elsewhere is
touched. Use the NFD-decompose → strip-combining-marks (`unicode.Mn`) →
NFC-recompose pattern via `golang.org/x/text/unicode/norm`,
`golang.org/x/text/runes`, `golang.org/x/text/transform`. Promote
`golang.org/x/text` from indirect to direct with `go mod tidy` — if that
pulls in anything unexpected or bumps an unrelated version, stop and report
it rather than fighting it. On a transform error, fall back to the
untransformed lowercased string rather than failing the match.

Leave `squash()` (username/email-local-part folding) untouched — usernames
are effectively always ASCII. If you find a concrete reason mid-
implementation that `squash` needs the same treatment, name it in the
report; do not change it silently.

## 3. Tests

Add table-driven cases to `rosteragent_test.go` (extend, don't replace):

- `"José García"` vs. `"Jose Garcia"` → exact match.
- Same visible name, two Unicode encodings — precomposed (`U+00E9`) vs.
  decomposed (`U+0065 U+0301`) — must normalize identically and match.
- `"Über Mueller"` vs `"Uber Mueller"` (umlaut, same mechanism).
- A same-name collision only visible after folding (e.g. `"José A. Ruiz"` /
  `"Jose B. Ruiz"`) still narrows by initial or falls back to
  `MatchAmbiguous` — folding feeds the CC-CA11 tiering, doesn't bypass it.
- A plain ASCII name (`"Jane Doe"`) is unaffected — identical match result
  and `Why` string to before. This is the regression guard.

## 4. Verify

- `go build ./...`, `go vet ./...`, `gofmt -l .` (must print nothing).
- `go test ./... -race -count=1` — full suite, not just `rosteragent`.
- `go.mod`/`go.sum` diff is exactly the `golang.org/x/text` indirect→direct
  promotion — no unrelated dependency churn.

## 5. Report

- Whether CC-CA13 was already implemented before this prompt ran, and if
  so, that no changes were made.
- If implemented here: the diff, and confirmation every CC-CA10/CC-CA11
  test still passes unchanged.
- The precomposed-vs-decomposed test result specifically.
- What was decided about `squash()` and why.
- Full command output for every command in §4, pass or fail.
