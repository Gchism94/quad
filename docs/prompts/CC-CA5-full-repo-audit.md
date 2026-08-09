# CC-CA5 — Full-repo audit: verify Cairn actually works as the docs now claim

*Claude Code prompt. Authored in Cowork, 2026-08-09, after CC-CA4 landed
(commit `09eae99`) and after `CLAUDE.md` + `.claude/commands/new-cc-prompt.md`
were added to this repo (commits `008a609`, `d5f5671`). What's already
verified: CC-CA4's two specific claims (grade-import fidelity 11/11;
retroactive autograding gated on Classroom-UI registration, not deadline).
What this prompt needs to resolve: whether the *rest* of the repo — build,
tests, the other CC-CA1–CA3 changes, and the docs written across this whole
migration-guide effort — is actually consistent and working, not just the
two things CC-CA4 happened to touch. This is a verification pass, not a
feature prompt; expect it to mostly confirm things, and to flag anything it
can't.*

## 1. Read first

- `CLAUDE.md` (the platform invariant and CC-* namespace rule now apply to
  this prompt too)
- `ROADMAP.md`, `docs/ghc-import.md`, `docs/migrate-from-github-classroom.md`
  — the docs this audit is checking against reality
- `docs/prompts/CC-CA1-gvisor-isolation-tier.md`,
  `CC-CA2-migration-guide.md`, `CC-CA3-invite-link-and-student-landing.md`,
  `CC-CA4-retroactive-autograding-poc.md` — what was claimed done

## 2. Build and test, for real

- `go build ./...` and `go vet ./...` — clean?
- `go test ./... -race -count=1` — full pass? Note any skipped tests and why.
- `cd web && npm ci && npm run build` (or the repo's actual build script —
  check `package.json`) — clean?
- `npm run lint` / `npm run typecheck` if those scripts exist — clean?
- If anything fails, don't fix it silently — report the failure verbatim
  first, then decide with Greg whether it's pre-existing or a regression
  from this session's changes.

## 3. CC-CA1–CA3 reality check

For each, confirm the ROADMAP/prompt's claim matches the actual code, not
just the doc:

- **CA1 (gvisor isolation tier)** — is it actually wired into the code path
  that runs student code, or just present as config/scaffolding?
- **CA2 (migration guide)** — does `cairn import ghc` still behave as
  `docs/ghc-import.md` describes? Spot-check the `--no-grades` flag and the
  idempotency claim (running import twice produces no duplicates).
- **CA3 (invite link + student landing)** — click through both fixes if
  there's a way to do it without live GitHub Classroom creds (a local
  server + unit/integration test is fine); confirm the student-landing fix
  doesn't regress the instructor path.

## 4. Docs-vs-code consistency

- Confirm the `/grades` endpoint really is never called anywhere in the
  import path (`grep -rn "grades" internal/ cmd/` and check every hit is
  either the rejected-endpoint rationale or the `accepted_assignments[].grade`
  field, not a live call to `/classrooms/:id/grades`).
- Confirm `docs/ghc-import.md`'s claim that autograding-registration state is
  only visible via the starter repo's `classroom.yml` + the assignment's
  `passing` count (from CC-CA4) doesn't contradict anything in the actual
  import code's assumptions.

## 5. Repo hygiene

- `git log -20 | grep -c Co-Authored-By` → should be `0`.
- `git status` — is anything uncommitted beyond the known local
  `.claude/settings.json` / `.claude/settings.local.json`? If yes, don't
  commit it blind — report what it is first.
- Confirm `/context` shows `CLAUDE.md` loaded, and that `/new-cc-prompt`
  appears as an available command.

## 6. Report

Report back:

- Build/vet/test results, verbatim for anything non-clean
- Web build/lint/test results, verbatim for anything non-clean
- CA1–CA3: confirmed-as-claimed, or divergence found (be specific)
- The `/grades` grep result and whether it's clean
- Repo hygiene status
- Anything you could not verify and why (e.g., no live Classroom
  credentials available for an end-to-end CA3 click-through)

Do not commit any fixes without listing them first — this prompt is a
verification pass; if it finds something broken, stop and report before
changing code.
