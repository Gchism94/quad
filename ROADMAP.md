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
- [x] Durable persistence — PostgreSQL store (`internal/store/postgres`, database/sql) behind `-tags postgres`; in-memory remains the default build
- [x] Operator authentication — host OAuth + username allowlist, cookie sessions, `created_by` attribution; opt-in via `QUAD_ADMIN_USERS` (open mode otherwise, with a warning)

## Phase 2 — Host-agnostic *(the differentiator)*
- [x] Forgejo/Gitea adapter — working end to end (README; live-validated)
- [x] GitLab adapter — adapter + OAuth resolver + webhook wiring (commit 5388baf)
- [ ] Generalized auth across hosts — remaining polish

*(Checkboxes updated 2026-07-21 to match commits and README; the file had drifted.)*

## Pilot sprint — July–August 2026 (the 1.0-beta gap; see KICKOFF-PROMPT.md)
Deadline context: pilot-ready before the August 28 shutdown; the pilot course
integrates for the full semester from the first assignment.
- [ ] GitHub Classroom import (`quad import ghc`) — the migration path
- [ ] `quad doctor` self-diagnostics
- [ ] One-command deploy verified on a clean VM (docs/deploy.md)
- [ ] Student-facing views completed
- [ ] docs/migrate-from-github-classroom.md — the afternoon migration guide

## Phase 3 — Ephemeral LMS roster agent
- [ ] Open, auditable local agent (browser extension / CLI)
- [ ] Instructor-token API pull (Canvas/Moodle/Brightspace); DOM scrape fallback
- [ ] Local-only name↔username matching; server receives username (+ email hash) only

## Phase 4 — Hosted + LMS integration *(stretch)*
- [ ] Multi-tenant hosted offering (scoped data processor)
- [ ] LTI 1.3 Names-and-Roles (NRPS) roster sync
- [ ] LTI Assignment-and-Grade Services (AGS) grade passback
