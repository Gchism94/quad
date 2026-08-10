# CC-CA13 — Fold accented/Unicode names in the roster matcher

*Claude Code prompt. Authored in Cowork, 2026-08-10, following CC-CA11's own
flagged follow-up (`ROADMAP.md`, Phase 3 section): "matching is
ASCII/case-folding only, so an accented name (e.g. 'José') won't match at
all — likelier to matter on a real roster than the initials case."*

## 1. The gap

`internal/rosteragent/rosteragent.go`'s `normalizeName` is:

```go
func normalizeName(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(s)), " "))
}
```

It lowercases and collapses whitespace, nothing else. Every comparison in
`matchOne` — the exact-name check, `sameNameParts`, `initialsOf` — normalizes
through this one function, so the gap is centralized, not scattered. But
`strings.ToLower("José")` is still `"josé"`, not `"jose"`. A student named
José on the LMS roster and `Jose` (or `José` typed with a different Unicode
form — see below) as their Git-host display name will not match at any
tier: not exact, not `sameNameParts`, not the initial narrowing. They fall
all the way through to `MatchNone` and the instructor has to hand-resolve
someone the agent should have matched cleanly.

**This is a real, not hypothetical, gap for a live roster.** Names with
diacritics, umlauts, and similar marks are common; the ASCII-only
middle-initial case CC-CA11 fixed is comparatively rare.

## 2. The fix

Fold diacritics out during normalization, so comparison is accent-insensitive
while display/audit strings elsewhere are untouched (only `normalizeName`'s
*comparison* output changes — never show a folded name to a human).

`golang.org/x/text v0.29.0` is already in `go.mod` as an **indirect**
dependency (pulled in transitively today). Import
`golang.org/x/text/unicode/norm`, `golang.org/x/text/runes`, and
`golang.org/x/text/transform` directly in `rosteragent.go` and run `go mod
tidy` to promote it to a direct requirement — this should not add a new
module, just move an existing one from indirect to direct. If `go mod tidy`
*does* pull in something new or bumps a version unexpectedly, stop and report
that rather than fighting it.

Standard NFD-decompose / strip-combining-marks / NFC-recompose pattern:

```go
import (
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
	"unicode"
)

func stripDiacritics(s string) (string, error) {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	return out, err
}
```

`normalizeName` should apply this after lowercasing (order matters less than
doing both), and should **not** silently swallow a transform error — on the
rare malformed-input error, fall back to the untransformed lowercased string
rather than failing the whole match, since a roster row failing to parse is
worse than a row that's merely unfolded.

Do **not** touch `squash()` — it compares email local-parts and Git usernames,
which are effectively always ASCII by the nature of what a username is, and
folding there would be scope creep beyond what CC-CA11 flagged. If you find a
concrete reason mid-implementation that `squash` needs the same treatment,
name it in the report rather than changing it silently.

## 3. Tests

Add table-driven cases to `rosteragent_test.go` (extending, not replacing,
the CC-CA10/CC-CA11 tests — run the existing suite after your change and
confirm every one of those still passes unchanged):

- `"José García"` (LMS row) vs. candidate `FullName: "Jose Garcia"` → exact
  match, same as an unaccented name today.
- **The subtler real bug**: the *same* visible name, `"José"`, encoded two
  different ways — precomposed (`U+00E9`, a single "é" codepoint) on one side
  and decomposed (`U+0065 U+0301`, "e" + a combining acute accent) on the
  other. These render identically and a human can't tell them apart, but a
  byte-for-byte or naive-lowercase comparison treats them as different
  strings. An LMS export and a Git host's stored profile name have no reason
  to agree on which form they use. Confirm both forms normalize to the same
  result and match each other.
- `"Über Mueller"` vs `"Uber Mueller"` — umlaut, not an accent, same
  mechanism.
- A same-name collision where the tie is only visible *after* folding (e.g.
  two candidates `"José A. Ruiz"` / `"Jose B. Ruiz"`) still correctly narrows
  by initial or falls back to `MatchAmbiguous` — folding must not bypass the
  CC-CA11 tiering, only feed it better-normalized input.
- A plain ASCII name (e.g. `"Jane Doe"`) is unaffected — same match result,
  same `Why` string, as before this change. This is the regression guard.

## 4. Verify

- `go build ./...`, `go vet ./...`, `gofmt -l .` (must print nothing).
- `go test ./... -race -count=1` — full suite, not just `rosteragent`.
- Confirm `go.mod`/`go.sum` diffs are exactly what promoting `golang.org/x/text`
  to direct should look like — no unrelated dependency churn.

## 5. Report

- The diff, and confirmation every CC-CA10/CC-CA11 test still passes
  unchanged.
- The precomposed-vs-decomposed test result specifically — this is the case
  most likely to be news to whoever reads the report, since it's not the
  "obviously accented" case people picture first.
- Anything about `squash()` or username folding you noticed but deliberately
  left alone, per §2.
- Full command output for every command in §4, pass or fail.
