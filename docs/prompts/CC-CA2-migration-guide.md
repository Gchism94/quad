# CC-CA2 — The migration guide, executed rather than written

*Claude Code prompt. Authored in Cowork, 2026-08-08. This is the last
pilot-blocking item before the GitHub Classroom shutdown, and it is a document
rather than code, which is exactly why it is easy to under-invest in and easy to
get wrong.

**Dates that make this urgent, and that belong in the guide itself:** GitHub
Classroom sign-ups are already disabled. Full shutdown is **August 28, 2026** —
twenty days from this prompt's authoring. Final data deletion is **September 4**.
After that date an instructor who has not exported cannot recover their roster or
assignment structure from GitHub at all. The guide is the artifact that converts
an instructor inside that window, and its real competition is not another tool —
it is the instructor deciding the migration looks like too much work and taking
the loss.

**A state correction, verified before writing this prompt.** `ROADMAP.md` lists
two open pilot-sprint items. Only one is actually open:

- *"Student-facing views completed"* — **already done.** `internal/api/student.go`
  (own-data-scoped `/me/work` and `/me/work/{submissionID}`) and
  `internal/api/studentpage.go` (the framework-free `/me` page: assignment list,
  repo link, deadline state, grade status, per-test breakdown, attempt history,
  polling while grading settles, XSS-escaped, dark-mode) cover exactly what
  `KICKOFF-PROMPT.md` Session 3 item 1 asked for. Seven tests back it, including
  cross-student isolation, unauthenticated 401, and privilege-escalation refusal.
  The checkbox was never ticked. **Tick it as part of this work — after §2's
  end-to-end check, not before.**
- *"docs/migrate-from-github-classroom.md"* — genuinely absent. This prompt.

**The acceptance bar, from `KICKOFF-PROMPT.md` Session 3, quoted because it is
the whole point:** *"the guide has been executed as written, not just read."* A
guide that has never been run is a hypothesis. Do not write this from the source
tree and existing docs alone.*

---

## 1. Read first

- `docs/ghc-import.md` — the `cairn import ghc` reference, already detailed
  (13.6 KB), including the export surface verified against a real classroom and
  the rejected `/grades` endpoint. **This guide does not duplicate it.** That doc
  is the reference; this one is the narrative path, and it should link rather
  than restate.
- `docs/deploy.md` — the one-command deploy, verified end to end on a bare
  Ubuntu 24.04 droplet on 2026-08-06 (Docker install → build → `compose up` in
  2m06s → `cairn doctor` green). Same relationship: link, don't restate.
- `docs/github-setup.md` — GitHub App creation and credentials.
- `internal/api/studentpage.go` and `student.go` — what the student actually
  sees, so the guide describes it accurately rather than aspirationally.
- `KICKOFF-PROMPT.md` Session 3, and `ROADMAP.md`'s pilot-sprint block.

## 2. Execute the path before documenting it

Run the whole thing yourself, in order, on a real (or realistically seeded)
classroom. Record what actually happens, including the parts that are slower or
uglier than expected:

1. Deploy Cairn (per `docs/deploy.md`).
2. `cairn doctor` green.
3. GitHub App credentials configured.
4. `cairn import ghc` against a real classroom — capture the snapshot.
5. Verify the roster landed and matches.
6. Create or confirm the first assignment.
7. **A student accepts it** — an assignment link, a self-claim join, a repo
   provisioned.
8. **The student sees their own work at `/me`** — this is the end-to-end student
   check that closes the stale ROADMAP box. Confirm with a dummy account that is
   *not* the operator account, since the interesting failure is a student seeing
   another student's work or an operator route.
9. A grading run completes and the score surfaces on the student page.

**Where a step does not work, that is a finding, not an obstacle to route
around.** Fix it if it is small and in scope; report it plainly if it is not. A
migration guide that quietly omits a broken step is worse than no guide, because
the instructor hits the break alone, at the deadline, with their course on the
line.

## 3. Write it for the instructor, not the sysadmin

The reader is a faculty member with a GitHub Classroom course, limited time, and
no particular appetite for Docker. Structure it as the afternoon path
`ROADMAP.md` promises:

**Open with the deadline and the irreversible bit.** August 28 shutdown,
September 4 deletion. Then the single most important instruction in the
document: **capture the import snapshot before September 4, even if the rest of
the migration happens later.** The snapshot outlives GitHub's deletion; the
opportunity to take it does not. An instructor who reads only the first screen
should still come away having done the one thing that cannot be undone later.

Then: what you need before starting (accounts, access, roughly how long) →
deploy → import → verify the roster → first assignment → what students do →
what students see. End with what is different from GitHub Classroom, honestly —
including what Cairn does not do yet. An instructor who discovers a gap after
migrating trusts nothing else in the document.

Keep the prose in this repo's existing register: direct, specific, no marketing.
`docs/ghc-import.md` and `docs/deploy.md` are the models — match them.

## 4. Include the things guides usually omit

- **What the snapshot actually preserves**, and what it does not.
- **The multi-host escape hatch.** Cairn's differentiator is that it is not
  GitHub-only — Forgejo/Gitea and GitLab adapters both work. An instructor
  migrating *off* GitHub Classroom is a natural audience for "and you are not
  locked to GitHub afterwards either." One short section, linked to the
  per-host setup docs, not a rewrite of them.
- **Rollback.** What happens if they start and stop halfway. Say it plainly.
- **The one-week-out version.** A reader in late August has no time for the full
  path; give them the minimum ordered sequence that gets a course running, even
  if it defers polish.

## 5. Report

- Confirm you executed the path end to end, and say on what (real classroom,
  seeded fixture, or a mix — be specific about which steps were which).
- Every place reality diverged from the documented happy path, listed. This is
  the most valuable part of the report; do not compress it.
- Whether the student end-to-end check (§2 steps 7–9) passed with a non-operator
  account, and the ROADMAP box updated accordingly — both boxes, with the
  student-views one noted as already-built-but-untracked rather than newly done,
  so the history stays honest.
- Anything you found that should block the pilot and is not in `ROADMAP.md`.
- `go test ./...` and `go vet ./...` green if any code changed; if nothing
  changed, say so explicitly rather than omitting it.
