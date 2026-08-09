# CC-CA6 — Bulk manual roster entry: the fallback that has to work regardless of LMS

*Claude Code prompt. Authored in Cowork, 2026-08-09, from Greg's direction on
Phase 3 ("LMS-roster agent for the pilot would be best, but have a manual
version as well in case LMS doesn't work with it — Classroom, for instance,
didn't have Brightspace"). What's already verified by reading the code:
manual roster entry already exists end to end —
`POST /classrooms/{id}/roster` (`handleAddRoster`, `internal/api/server.go`
~588) backed by `store.CreateRosterEntry`, surfaced in
`web/src/components/RosterPanel.tsx` via a single username input and an "Add
student" button. What's missing: it only takes one student at a time. That
is not a usable fallback for a real class roster (20-150 students) — an
instructor whose LMS the CC-CA7 agent can't reach (the Brightspace precedent
Greg named is real: GitHub Classroom itself never supported it) needs to
paste or upload a whole roster at once, not type each name in.

This prompt ships the fallback. It has no dependency on CC-CA7 and should
land first — it's the thing that guarantees the pilot can onboard a roster
no matter what CC-CA7 turns out to support.*

## 1. Read first

- `internal/api/server.go` — `handleAddRoster` (~588) and the route table
  (~170-171) for the existing single-add contract; match its validation
  (`validSlug`) and response shape rather than inventing a new convention
- `internal/store/models.go` — `RosterEntry` (~80), especially the
  `EmailHash` field's doc comment: it's already designed for "client-side
  re-matching against an LMS pull," so a bulk endpoint should accept it
  optionally per-row, the same as the single-add endpoint does
- `internal/store/store.go` — confirm `FindRosterEntryByUsername` is the
  right dedup check (avoid creating duplicate entries if the same roster is
  pasted twice, e.g. after fixing one bad row)
- `web/src/components/RosterPanel.tsx` — the existing single-add UI this
  needs to sit alongside, not replace (some instructors will still want to
  add one late-adding student without re-pasting the whole list)
- `web/src/api.ts` — the `addRoster` client function and `RosterEntry` type,
  for the idiom a new bulk client function should follow

## 2. Backend — a bulk endpoint, not a bulk-only replacement

Add `POST /classrooms/{id}/roster/bulk` accepting a list of
`{username, email_hash?}` rows (don't change the existing single-add
endpoint's contract). Process every row independently — one bad row (invalid
`username`, fails `validSlug`) must not block the other rows in the same
request. Return a per-row result: `created`, `already_present` (dedup via
`FindRosterEntryByUsername`), or `error` with the specific reason. This
"partial success, not all-or-nothing" behavior is the actual design
requirement — an instructor pasting 30 names should get 29 successes and one
clear error to fix, not a single failure for the whole batch.

Cap row count generously but not unboundedly (e.g. 500) and reject the whole
request above that with a clear error, so a malformed paste (wrong file
entirely) fails fast rather than partially importing garbage.

## 3. Frontend — paste, don't just type

Add a bulk-add affordance to `RosterPanel.tsx`, next to the existing
single-add input (a toggle or a second collapsed section is fine — don't
replace the one-at-a-time input, some instructors will still use it). Accept
either:
- one username per line, or
- simple two-column CSV (`username,email` or `username,email_hash` — decide
  which based on what's more useful given `email_hash`'s "hash it locally,
  never send plaintext" design intent; if a plaintext email column is
  accepted, hash it client-side before the request, never send the raw
  address to the API)

After submit, show the per-row results from §2's response — a clear table of
created / already-present / error, not just a single toast. This is the part
an instructor actually needs to see to fix a typo without guessing which
name failed.

## 4. Tests

- Bulk add with a mix of valid, invalid, and duplicate-of-existing rows
  returns the correct per-row status for each, and valid rows are created
  even when others in the same request fail.
- Re-submitting the same bulk list twice produces no duplicate roster
  entries the second time (idempotency, matching the single-add path's
  existing behavior).
- The existing single-add endpoint and its tests are unaffected.
- Frontend: parses both plain-username-per-line and CSV input correctly;
  renders per-row results.

## 5. Report

- The exact request/response shape chosen for the bulk endpoint.
- Confirm partial success works (some rows created, others rejected, in one
  request) with a concrete example from a test.
- Whether email was accepted as plaintext-then-hashed client-side or
  email_hash-only, and why.
- `go test ./...`, `go vet ./...`, and the frontend build/typecheck all
  green.
