# CC-CA4 — Retroactive autograding: prove it works (or that it doesn't) before the guide claims it does

*Claude Code prompt. Authored in Cowork, 2026-08-09, following directly from
CC-CA2's zero-grades finding on INFO-526-SU26 and Greg's correction to it.

**Where this comes from.** CC-CA2 found 202/202 imported submissions with no
grade on INFO-526-SU26. The migration guide's §8 documents an optional
backfill (add autograding tests retroactively, trigger a run, re-import), but
**that path has never actually been run — it is a documented theory.** Two
things Greg added that change the shape of this work:

1. **GitHub Classroom has a known, long-standing problem computing/recording
   grades after an assignment's deadline has passed** — this is why grades
   were rarely present to begin with, independent of whether a course used
   the Autograder at all. This means retroactive autograding may not simply
   work; it may run into the exact same post-deadline failure mode Classroom
   has always had. The guide currently doesn't account for this at all.
2. **Some of Greg's real classrooms already have non-null grades** in
   `accepted_assignments[].grade`. Those are a control CC-CA2 never had —
   its tested classroom had zero grades, so the "grades import correctly"
   happy path was never actually exercised against real data, only asserted.

This prompt has two parts, in order, and the docs get corrected only after
both are done — not before.

---

## 1. Control: confirm import fidelity where grades already exist

Pick one real classroom/assignment of Greg's that has existing non-null
`accepted_assignments[].grade` values (ask which one, or find one via
`cairn import ghc --list` plus the Classroom UI/API — Cowork does not have
live access to enumerate these, so this session needs to pick a concrete
target itself).

Before importing: record a handful of ground-truth values directly from the
Classroom API or UI (student, score, max — 3-5 rows is enough, not the whole
roster). Import (or `--dry-run` then import), then compare the resulting
Cairn `Grade` rows against those recorded values exactly — `score`,
`max_score`, and `breakdown.raw`. This closes a real gap: grade import
fidelity against data known to exist has never been checked, only claimed.

## 2. Experiment: does retroactive autograding actually work post-deadline?

Pick **one** assignment whose deadline has already passed and whose grade is
currently null. Keep this small and contained — 2-3 already-accepted repos,
not the whole class, since this is a live write against Greg's real GitHub
org, unlike anything in CC-CA1-3.

- If the assignment has no autograding tests configured, add a minimal one
  via Classroom's Autograding UI (even a trivial "does this file exist"
  check — the point is not test quality, it's whether the pipeline runs and
  reports at all).
- Trigger the workflow against the small sample — a `workflow_dispatch` if
  the Action supports it, or an obviously-marked no-op commit (a comment or
  a clearly-labeled test file, not a change to existing student code, so it
  can be identified and reverted later; note exactly what was pushed).
- Once the Action run completes on GitHub, re-query
  `GET /assignments/:id/accepted_assignments` — the same source Cairn's
  importer reads — and check whether `grade` is now populated for the
  tested repos.
- **Distinguish the two possible failure points, precisely:** (a) the
  workflow doesn't run at all on an old, past-deadline repo (branch
  protection, archival, or some other post-deadline restriction on GitHub's
  side), versus (b) it runs and computes a score fine, but Classroom's own
  `accepted_assignments` API never reflects it. Greg's "always had a problem
  after deadlines" could be either — find out which, since they imply
  different things about whether this is fixable at all from Cairn's side
  (neither is, actually — both are entirely GitHub's behavior — but the
  distinction still matters for how honestly the guide can describe what an
  instructor should expect).
- Re-run `cairn import ghc` (idempotent) against just that classroom and
  confirm whether the previously-null `Grade` rows for the tested repos are
  now populated, matching whatever the retriggered Autograder actually
  computed (or confirm they're still null, if that's what happens).

## 3. Report, then correct the docs — not before

Report plainly:

- The control result from §1 — did every recorded value match exactly?
- The experiment result from §2 — which of (a)/(b) happened, or whether it
  worked cleanly. Include the exact repos/assignment touched, since this is
  the first CC-CA prompt to write to live student repos.
- Only after this is known, update `docs/migrate-from-github-classroom.md`
  §8, `docs/ghc-import.md`, and the `ROADMAP.md` caveat to state — with real
  evidence, not theory — whether the backfill path reliably works, partially
  works, or doesn't work at all. If it doesn't work, say so plainly and
  remove or heavily caveat the "optional path" language rather than leaving
  untested advice in an instructor-facing guide.

## 4. Safety notes

- This is the first CC-CA prompt that writes to real student repos on
  GitHub, not just Cairn's own code or store. Keep the sample small (2-3
  repos) and confirm the target course is genuinely finished (INFO-526-SU26's
  deadlines are all past) before pushing anything.
- Mark any triggering commit obviously as a test artifact so it's easy to
  identify and revert afterward if Greg wants it gone.

## 5. Read first

- `docs/migrate-from-github-classroom.md` §8 — the current, untested backfill
  language this prompt exists to verify or correct
- `docs/ghc-import.md` — grades provenance/limits, and the backfill note
  added alongside it
- `ROADMAP.md`'s zero-grades caveat
- `internal/store/models.go` for the `Grade` shape being compared against in §1
