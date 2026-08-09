# Roadmap

Timeline context: GitHub Classroom sign-ups are already disabled; full shutdown
is **August 28, 2026**, with final data deletion September 4. The differentiator
(host-agnosticism + privacy) should land early enough to matter to educators
migrating now.

## Phase 1 — MVP (GitHub only)
The full vertical slice on one host, with the adapter interface and data model in
their final shape:
- [x] GitHub App auth (installation tokens minted on demand)
- [x] Implement `pkg/adapter/github` methods (DispatchGrading intentionally deferred to the runner path)
- [x] Classrooms; assignments from a template repo (individual; group is additive)
- [x] Student self-claim join flow (host OAuth → bind username only)
- [x] Repo provisioning via the idempotent, rate-limited queue (worker drains jobs against the adapter)
- [x] Deadlines (scheduler auto-locks repos at the deadline; manual lock/unlock endpoints for early close / extensions)
- [x] Autograding + score capture (pluggable Runner + Checkout; persists GradingRun & Grade). Sandboxed **container runner** enforces gradingspec.Limits (network/memory/cpu/pids, dropped caps, read-only rootfs); host-exec runner remains as an explicit unsafe/local option
- [x] CSV export of scores keyed by username
- [x] Web dashboard — instructor console (React/TS + Vite in web/); student-facing views are follow-up
- [x] Durable persistence — PostgreSQL store (`internal/store/postgres`, database/sql), selected by `CAIRN_DATABASE_URL` / `CAIRN_STORE=postgres`; SQLite is the zero-config default and in-memory is available for tests. (No build tag: the pgx driver is always compiled in.)
- [x] Operator authentication — host OAuth + username allowlist, cookie sessions, `created_by` attribution; opt-in via `CAIRN_ADMIN_USERS` (open mode otherwise, with a warning)

## Phase 2 — Host-agnostic *(the differentiator)*
- [x] Forgejo/Gitea adapter — working end to end (README; live-validated)
- [x] GitLab adapter — adapter + OAuth resolver + webhook wiring (commit 5388baf)
- [ ] Generalized auth across hosts — remaining polish

*(Checkboxes updated 2026-07-21 to match commits and README; the file had drifted.)*

## Pilot sprint — July–August 2026 (the 1.0-beta gap; see KICKOFF-PROMPT.md)
Deadline context: pilot-ready before the August 28 shutdown; the pilot course
integrates for the full semester from the first assignment.
- [x] GitHub Classroom import (`cairn import ghc`) — the migration path. Reads the
  Classroom REST API directly (no export script), captures a snapshot that outlives
  the September 4 deletion, and binds Cairn to the existing org repos without
  touching GitHub. Export surface verified against a real classroom first; findings
  and the rejected `/grades` endpoint are documented in `docs/ghc-import.md`
- [x] `cairn doctor` self-diagnostics — store reachability (distinguishing
  unmigrated from unreachable), unrecognized/ignored `CAIRN_*` variables, listen
  address, host credentials (`--verify-hosts` proves them against the host),
  webhook config, operator auth, dashboard, container runtime, and a real
  round-trip proving grading can see its work directory. Every failure prints the
  fix; exit status 1 on failure
- [x] One-command deploy verified on a clean VM (docs/deploy.md) — `Dockerfile` +
  root `compose.yaml` + `deploy/docker-compose.yml` (cairn + PostgreSQL, Caddy
  under `--profile tls`). Verified end to end on a bare Ubuntu 24.04 droplet
  2026-08-06: Docker install → build → `docker compose up -d` (2m06s) → `cairn
  doctor` green → classroom created in the dashboard; grading's socket + shared
  work directory confirmed on native Linux; survives reboot with data intact
- [x] Student-facing views completed *(already built — `internal/api/student.go`
  + `studentpage.go`, 7 tests backing isolation/401/404/privilege refusal; the
  checkbox was never ticked. Marked during CC-CA2 after an end-to-end check
  against a real classroom.)*
- [x] docs/migrate-from-github-classroom.md — the afternoon migration guide
  *(CC-CA2, executed against a real classroom before writing)*

**Known, expected behavior, surfaced by CC-CA2's real-classroom run —
not a defect and not unique to that classroom:** a course that graded
outside GitHub Classroom's own Autograder imports with **zero grades**,
because `accepted_assignments[].grade` is simply null for every submission —
there was nothing in Classroom to bring over (202/202 in the tested
classroom, confirmed by Greg as a course that never had grades in Classroom
at all). This will be the common case across migrating courses, not the
exception, and the guide states it plainly rather than treating it as a bug.
An optional, non-default backfill path (retroactive autograding, then
re-import) is documented in `docs/migrate-from-github-classroom.md` §8 and
`docs/ghc-import.md` for instructors who specifically want an autograded
score — it computes a new score, not a recovery of an original one, and most
instructors migrating a finished course won't want it.

**That backfill path is untested.** Greg: Classroom has a known,
long-standing problem computing/recording grades after a deadline has
passed, which is why grades were rarely present at all — so retroactive
autograding may hit the same failure post-deadline rather than simply
working. `docs/prompts/CC-CA4-retroactive-autograding-poc.md` is queued to
run it for real against a small sample (2-3 repos) of one of Greg's real
past-deadline classrooms, plus a control check against a classroom that
already has real grades (to confirm import fidelity on data known to exist,
which CC-CA2 never got to check). The docs get corrected with real evidence
once that runs. Not pilot-blocking.

## UX polish — queued 2026-08-09, from an instructor/student review
Not pilot-blocking, but cheap and worth doing before the pilot term starts.
See `docs/prompts/CC-CA3-invite-link-and-student-landing.md`.
- [ ] Copy-invite-link action on each assignment card (the join URL is a real
  route already — `/assignments/{id}/accept` — but nothing in the UI surfaces
  it, so instructors hand-build it)
- [ ] Stop serving the instructor login screen at `/` to a student who is
  already signed in. Confirmed in code: a valid student session 401s against
  `/auth/me` (operator-only), so the SPA shows "sign in" to someone already
  authenticated, with no link to `/me` or `/student/login`.

## Phase 3 — Ephemeral LMS roster agent
- [ ] Open, auditable local agent (browser extension / CLI)
- [ ] Instructor-token API pull (Canvas/Moodle/Brightspace); DOM scrape fallback
- [ ] Local-only name↔username matching; server receives username (+ email hash) only

## Phase 4 — Hosted + LMS integration *(stretch)*
- [ ] Multi-tenant hosted offering (scoped data processor)
- [ ] LTI 1.3 Names-and-Roles (NRPS) roster sync
- [ ] LTI Assignment-and-Grade Services (AGS) grade passback
