# CC-CA15 — Purge-after-confirmed-export and roster-entry deletion

*Claude Code prompt. Authored in Cowork, 2026-08-10. Not a hardening
nice-to-have — this closes the two items `PRIVACY.md` and
`../educloud/docs/policy/data-destinations.md` §8 name as genuinely
outstanding before real student rosters land on production infrastructure:*

> "No retention or purge path exists... Grades accumulate indefinitely. The
> intended policy is purge-after-confirmed-export; the implementation is
> outstanding." / "No roster-entry deletion with dependent rows. Required to
> support a removal request even though the LMS is the record of truth."

Greg's call on the destination question this doc also tracked: **D2 for
now** (self-provisioned infrastructure) — a UITS conversation about
institutional (D1) hosting may happen later but isn't imminent. That makes
this prompt more load-bearing, not less: there's no institutional retention
policy backing this up. Cairn's own purge behavior is what "working store,
not the gradebook of record" (`PRIVACY.md`) actually rests on.

## 1. Read first

- `PRIVACY.md` (full) and `data-destinations.md` §8 — the exact open items
  this prompt closes
- `DESIGN.md` §10 — compliance framing, marked `[DECIDE later, with
  counsel]`; a retention *duration* is a policy choice, not an engineering
  one, so pick a conservative default and say so explicitly rather than
  treating it as settled
- `internal/store/models.go` — `RosterEntry` (note `RosterRemoved` status
  already exists as an enum value but **no handler sets it today** — this
  prompt is about hard deletion, not that lifecycle flag; don't conflate
  them, and don't wire the status flip in unless you have a concrete reason
  to, in which case say so in the report rather than doing it silently),
  `Submission`, `Grade`, `GradingRun`
- `internal/store/migrations/0001_init.up.sql` — note `ON DELETE CASCADE`
  already runs `roster_entries → submissions → grades`/`grading_runs` at
  the schema level for Postgres; `internal/store/sqlite/sqlite.go`'s DSN
  already sets `_foreign_keys=on`, so SQLite enforces the same cascade.
  **The memory store does not get this for free** — it has no real FK
  engine, so its delete implementation must walk and remove dependents by
  hand.
- `internal/store/store.go` (the `Store` interface — every method is
  implemented three times: `postgres/`, `sqlite/`, `memory/`) and
  `internal/store/storetest/` — the shared conformance suite; new methods
  need cases here, run against all three backends, not one
- `internal/api/server.go`'s `handleGradesCSV` (the export path this
  confirms) and its routing table (`s.mux.HandleFunc(...)`) for the
  handler-registration pattern
- `internal/provisioning/scheduler.go` — the existing deadline-auto-lock
  scheduler. Mirror its shape (a `Run(ctx)` loop on a `time.Ticker`,
  idempotent per tick) for the purge job rather than inventing a new
  background-job pattern

## 2. Roster-entry deletion

- Add `DeleteRosterEntry(ctx context.Context, id string) error` to the
  `Store` interface, implemented in all three backends. Postgres/SQLite: a
  plain `DELETE FROM roster_entries WHERE id = $1` — the cascade already
  does the rest, confirm this with a test rather than assuming the pragma
  is honored end to end. Memory: explicitly delete every `Submission`
  referencing this roster entry, and every `Grade`/`GradingRun` referencing
  those submissions, before removing the roster entry itself.
- New endpoint: `DELETE /classrooms/{id}/roster/{entry_id}`, operator-only
  (same `protect()` wrapper as the rest of the operator surface). 404 if
  already gone (idempotent-safe to call twice), 204 on success.
- This is a genuine, irreversible delete — the row and every dependent
  submission/grade/grading-run vanish. It is **not** the same thing as
  marking someone `RosterRemoved` (dropped from an active roster while
  keeping their grade history for the term). This endpoint is for an actual
  removal/erasure request. State this distinction in the dashboard copy if
  you add a UI trigger (see §4), so an instructor can't reach for "delete"
  when they meant "drop from this term's active roster."

## 3. Purge-after-confirmed-export

- New migration (`0005_...`): nullable `grades.export_confirmed_at
  TIMESTAMPTZ`.
- New endpoint: `POST /classrooms/{id}/grades/confirm-export`. Sets
  `export_confirmed_at = now()` on every grade for that classroom's
  submissions that doesn't already have it set (idempotent re-calls are a
  no-op for already-confirmed rows). **Deliberately a separate, explicit
  instructor action from `GET .../grades.csv`** — the CSV download itself
  must not auto-start a retention clock, since a page reload or an
  automated fetch shouldn't be able to silently begin counting down toward
  deletion of data the instructor hasn't actually gotten into the LMS yet.
- New env var `CAIRN_GRADE_RETENTION_DAYS` (int, suggest defaulting to
  `30` as a conservative starting point — flag this default explicitly in
  the report as a policy choice for Greg/counsel to revisit, not something
  this prompt is authorized to decide permanently).
- A purge job matching `scheduler.go`'s shape: on each tick (once a day is
  plenty — this isn't the deadline scheduler's per-minute cadence), delete
  every `grades` row where `export_confirmed_at IS NOT NULL AND
  export_confirmed_at < now() - retention`. **Decide, and state the
  decision plainly in the report rather than picking silently:** does the
  matching `grading_runs` row (which may carry raw autograder output via
  `LogsRef`) purge on the same clock, or only the `grades` row itself?
  `grades.run_id` is a loose `TEXT` field, not an FK, so deleting a grade
  doesn't cascade to its grading run today — this is a real design
  decision the existing schema left open, not an oversight to silently
  paper over.
- Wire the purge job into `cmd/cairn`'s startup alongside the existing
  deadline scheduler.
- `cairn doctor` should report the retention configuration — `ok` with the
  configured day count, or `warn` if `CAIRN_GRADE_RETENTION_DAYS` is unset
  (purge effectively disabled), matching the doctor's existing habit of
  surfacing every consequential piece of config rather than staying silent
  about it.

## 4. Minimal UI trigger (don't skip silently — note if genuinely out of time)

Neither feature is usable in the pilot without *some* way for an instructor
to invoke it — API-only would ship half a feature. Add:
- A "Confirm export" action next to the existing CSV-download control,
  with copy that says plainly what it starts (a retention countdown, not
  an immediate delete).
- A delete/remove action on each roster row in `RosterPanel.tsx` (or
  wherever roster rows render today), worded to distinguish this
  irreversible delete from a lifecycle status change.

If time is genuinely short, backend-only is acceptable to land first — say
so explicitly in the report rather than quietly leaving the UI out.

## 5. Tests

Via `storetest`'s shared conformance suite (all three backends, not just
one):
- Deleting a roster entry removes its submissions, grades, and grading
  runs, and going through `GetRosterEntry` afterward returns `ErrNotFound`.
- `confirm-export` marks every current grade, is idempotent on repeat
  calls, and does not touch a grade created *after* the confirm call.
- The purge job removes only grades past the retention window with a
  confirmed export timestamp; grades with no `export_confirmed_at` are
  never touched regardless of age.
- `cairn doctor` reports the retention state accurately in both the
  configured and unset cases.

## 6. Report

- The retention-default decision and the `grading_runs`-purge decision from
  §3, stated explicitly with reasoning — these are the two real judgment
  calls in this prompt.
- Whether the UI trigger (§4) landed or was deferred, and why.
- Confirm the memory store's manual cascade actually matches
  Postgres/SQLite behavior (same conformance test, three backends, all
  green) rather than assuming the DB-level cascade is enough everywhere.
- Full `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .` output,
  plus frontend `npm test`/`npm run build` if §4 touched the dashboard.
