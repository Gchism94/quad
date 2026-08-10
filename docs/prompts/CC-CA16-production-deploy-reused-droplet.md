# CC-CA16 — Bring the reused droplet to a real deployment (open mode; no domain yet)

*Claude Code prompt. Authored in Cowork, 2026-08-10. Run this from a
terminal/session with actual SSH and network access — the droplet is not
reachable from Cowork's own sandbox tools.*

**Target:** `157.245.131.178` — the same DigitalOcean droplet used for the
Aug 6 deploy-verification session (`claude/cairn-session2-deploy-verified-2026-08-06.md`
in the Cowork project, worth reading first: it documents three real doc bugs
that surfaced only on a real droplet, and a credential-exposure incident,
below). This is now the **pilot destination for real** (Greg's call, D2 —
self-provisioned infrastructure — while a UA-hosted D1 option is a possible
later conversation with UITS, not an imminent one, per
`../educloud/docs/policy/data-destinations.md` §8).

**No domain is decided yet.** That means §8 (HTTPS) and §9 (OAuth + the
operator allowlist) in `docs/deploy.md` are **explicitly out of scope for
this prompt** — the server stays in Cairn's documented open mode (anyone who
can reach it can operate it) until a domain exists and a follow-up prompt
closes it. State this plainly in the dashboard/report rather than treating
it as done. Do not skip straight past this limitation — it's the reason
this deployment isn't yet safe for real student use, only for getting the
infrastructure real and correct.

## 1. Read first

- `docs/deploy.md` in full — the exact path this prompt follows
- `claude/cairn-session2-deploy-verified-2026-08-06.md` (Cowork project) —
  what already ran on this exact droplet, including a **credential-exposure
  incident**: `cairn.env` (gitignored local credentials) briefly touched
  disk via an `rsync` that didn't exclude it, was `shred -u`'d within the
  same minute, and never reached a container/image/commit — but "any
  credentials in that file should be treated as exposed and rotated." Treat
  that as still open until you've confirmed otherwise (§2).
- `PRIVACY.md` — the open-mode operational stance, so the report can state
  accurately what "open" means here
- `docs/prompts/CC-CA15-purge-after-export-and-roster-deletion.md` — if it
  has landed on `main` by the time this runs, deploy that commit; if not,
  say so plainly and recommend it land before real rosters are entered
  (though the infrastructure work here doesn't itself depend on it)

## 2. Inventory before touching anything

This droplet already ran a deploy once — don't assume a clean slate.
Probing it externally from Cowork's own (networked) sandbox just before
this prompt was written found:

- Port `8080`: unreachable — the earlier open-mode test exposure documented
  as a lock-it-down-or-tear-it-down open item appears gone.
- Port `80`: returns **HTTP 403** — something is currently serving there.
  Find out what (`docker ps -a`, `ss -tlnp`, `systemctl list-units
  --type=service --state=running`) and report it before deciding what to
  do about it — don't assume it's inert.
- Port `443`: no response externally (expected — no TLS profile has ever
  been enabled here).
- Port `22`: no response from Cowork's sandbox, but that's most likely a
  firewall scoped to known IPs rather than SSH being down — this prompt
  runs from a session that should already have real access.

Also check:
- `find / -iname "cairn.env" -o -iname "*.env" 2>/dev/null` (excluding the
  repo's own tracked `.env.example`) and shell history, to confirm the
  credential-exposure incident above is genuinely resolved. Anything found
  should be treated as exposed and rotated, not assumed safe because the
  prior report said it was cleaned up.
- `df -h`, `uptime`, `docker --version`, OS/kernel version — whether
  anything's drifted since Aug 6 in a way that matters.

**Recommendation, not a mandate — decide and state the decision:** since
this is about to become the real pilot deployment rather than another
disposable verification run, and the Aug 6 report describes only synthetic
test data on this droplet, a clean slate (`docker compose down -v` if a
compose project is found, otherwise manual container/volume/network
removal) is probably right. If you find anything that looks like it
shouldn't be thrown away, stop and say so rather than deleting it.

## 3. Deploy for real, per `docs/deploy.md` §§1–7

- Confirm Docker is installed and current (likely already true from Aug 6
  — verify rather than blindly reinstalling).
- Fresh `git clone` of current `main`; record the commit hash deployed.
- `/var/lib/cairn/work` per §3.
- `.env` per §4 — `POSTGRES_PASSWORD`, `DOCKER_GID`. Leave
  `CAIRN_ADMIN_USERS` unset; there's no OAuth app to pair it with yet
  (§9 is out of scope, per the framing above).
- `docker compose up -d`; confirm both services `Up`, `cairn` reaching
  `(healthy)`.
- `cairn doctor` — the acceptance bar is the same shape as Aug 6's: **no
  failures**, with the expected warnings for what's genuinely not
  configured yet (host adapter, operator auth, and — see below — possibly
  grading).
- Create a classroom via the dashboard over plain HTTP
  (`http://157.245.131.178:8080`) to confirm the deployment works end to
  end, matching the Aug 6 acceptance criterion.

**Grading (§10) — domain-independent, worth enabling now:** set
`CAIRN_GRADER=container` and a reasonable `CAIRN_GRADER_IMAGE`, then check
`cairn doctor`'s grading-workdir round-trip. Also check whether gVisor
(`runsc`) is available on this droplet (`CAIRN_GRADER_ISOLATION=gvisor` per
CC-CA1) — **report which tier ends up active and why**, defaulting to the
documented `shared` tier if gVisor isn't installed rather than treating
installing it as part of this prompt's scope. Installing gVisor is real,
separate work with its own footguns (see CC-CA1's own docs) — don't bundle
it in silently.

## 4. Explicitly out of scope for this prompt

- **§8 (HTTPS/domain) and §9 (OAuth + allowlist).** No domain is decided.
  The server is unauthenticated by design until one exists — that's
  Cairn's own documented open mode, not a bug, but it also means this
  deployment is not yet safe to point real students at. Say so plainly in
  the report. Closing this is the very next prompt once a domain exists.
- **Creating the GitHub App / host adapter.** Per `docs/github-setup.md`,
  this is a manual step in GitHub's own UI producing real client
  credentials — that's Greg's action to take, not this prompt's. If Greg
  supplies the resulting client ID/secret separately, wiring them into
  `.env` is fine; generating them is not in scope here.

## 5. Report

- Full inventory findings from §2: what was actually running (including
  what that port-80 403 turned out to be), whether any exposed credentials
  were found and what was done about them, what got cleaned up and why.
- The `cairn` commit hash deployed.
- Full `cairn doctor` output.
- Confirmation a classroom was created via the dashboard.
- The isolation-tier decision (§3) and why.
- An explicit list of what's deferred (§4) and what unblocks each item.
- If any real doc bug surfaced the way three did on Aug 6, fix
  `docs/deploy.md` itself and say so — don't just work around it silently.
- Nothing else in the repo should change; this is a deploy runbook
  execution, not a code change, apart from any doc fix above.
