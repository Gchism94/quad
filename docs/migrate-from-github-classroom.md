# Migrating from GitHub Classroom to Cairn

GitHub Classroom is being retired. Sign-ups are already disabled, the service
shuts down **August 28, 2026**, and classroom-specific data — your rosters,
assignment structure, and the mapping of student to repository — is
**permanently deleted September 4, 2026**. Accounts, organizations, and
repositories are unaffected and persist indefinitely.

If you do one thing after reading this, do this:

> **Capture the import snapshot before September 4, even if the rest of the
> migration happens later.** The snapshot outlives GitHub's deletion; the
> opportunity to take it does not. It is a handful of read-only requests and
> takes a minute per classroom. Everything else in this guide can wait; that
> cannot.

This guide is the afternoon path for an instructor with a GitHub Classroom
course, limited time, and no particular appetite for Docker. It was written
after running the whole path end to end against a real live classroom
(INFO-526-SU26, 30 students, 8 assignments, 202 submissions), not from the
source tree alone. Where reality diverged from the documented happy path, the
divergence is below, in line.

It does not restate the two reference docs it depends on — it links them:

- [`docs/deploy.md`](deploy.md) — the one-command deploy, every command in
  order, verified on a bare Ubuntu 24.04 droplet.
- [`docs/ghc-import.md`](ghc-import.md) — the `cairn import ghc` reference,
  including what the export surface actually is and the endpoint Cairn
  deliberately does not read.

---

## What you need before starting

- **A GitHub user token with the `read:org` scope.** The Classroom API is
  user-scoped — the GitHub App installation token Cairn uses elsewhere cannot
  read `/classrooms`. The simplest source is your own `gh` CLI:

  ```sh
  export CAIRN_GHC_TOKEN="$(gh auth token)"
  ```

  `gh auth status` should show your account and the `read:org` scope. If it
  does, you are ready to capture a snapshot immediately — even before you have
  a place to run Cairn.

- **The classroom ID.** `cairn import ghc --list` prints every classroom your
  token can see, with its numeric ID. The rest of the guide uses `<id>` for
  yours.

- **A Linux VM to run Cairn on**, eventually. A 2 GB / 1 vCPU instance is
  enough for a single course. You do **not** need this to take the snapshot —
  the snapshot is a CLI command that writes JSON to a directory. You can
  capture it on your laptop now and import it onto the VM later.

- **Roughly how long.** Snapshot capture: about a minute per classroom. Deploy:
  about fifteen minutes, most of it waiting for Docker to install. Import and
  verification: a few minutes. If you are reading this in late August with no
  time for the full path, jump to [One week out](#one-week-out).

---

## 1. Capture the snapshot now

This is the step that is irreversible if skipped, and it does not require a
running Cairn. You can do it from your laptop today.

```sh
cairn import ghc --classroom <id> --dry-run --snapshot ./ghc-<id>
```

`--dry-run` prints the complete plan and **writes nothing to the Cairn
store**. It does capture the snapshot directory — that is the point. Take one
of every classroom you may ever want, before August 28. The snapshot contains
GitHub usernames and repository names; it contains **no legal names**, because
the Classroom endpoint that carries them is never requested (see
[`ghc-import.md`](ghc-import.md) — the rejected `/grades` endpoint).

The snapshot is replayable after the API is gone:

```sh
cairn import ghc --from ./ghc-<id>
```

Copy the snapshot directory somewhere safe (a backup, a private repo). It is
now the only copy of that metadata.

---

## 2. Deploy Cairn

Follow [`docs/deploy.md`](deploy.md) end to end. It is the exact path — every
command is there, in order, and each was run on a fresh Ubuntu 24.04 machine.
The short version:

```sh
git clone https://github.com/EduCloud-Ecosystem/cairn.git
cd cairn
cp .env.example .env
# set POSTGRES_PASSWORD and DOCKER_GID per deploy.md §4
docker compose up -d
docker compose exec cairn cairn doctor
```

`cairn doctor` is the single source of truth for whether the deployment is
healthy. A brand-new deployment has **three warnings and no failures** — that
is the correct starting point. The warnings are the things the later sections
of `deploy.md` turn on (HTTPS, operator login, a Git host, grading).

Two things from `deploy.md` worth flagging here because they bite if missed:

- **Do not set `CAIRN_ADMIN_USERS` until you also have a working OAuth app.**
  Operator login needs both an allowlist *and* OAuth credentials together.
  Setting the allowlist alone locks out everyone, including you.
- **Run Cairn on a VM dedicated to it.** Grading mounts the host's Docker
  socket, and access to that socket is equivalent to root on the host. The
  grading containers themselves are tightly confined; the socket is the part
  to think about.

---

## 3. Import the course

With the snapshot in hand and Cairn running:

```sh
cairn import ghc --classroom <id> --snapshot ./ghc-<id>
```

or, if you are importing from a captured snapshot after the API is gone:

```sh
cairn import ghc --from ./ghc-<id> --created-by <your-github-username>
```

`--created-by` matters with `--from`: there is no live token to attribute the
import to, so pass your username explicitly or the import is unattributed.

The import is **idempotent** — re-running it creates nothing new. Rows match
on their natural keys (classroom by host + org, assignment by slug, roster
entry by username), so a re-run after a partial failure resumes safely. You
will see "existing" counts where before you saw "created".

What lands in Cairn, exactly: a `Classroom` (host github, join policy roster),
one `Assignment` per Classroom assignment (title, slug, type, deadline,
starter-code ref), a `RosterEntry` per student (**username only — no legal
name, no SIS id, no plaintext email**), a `Submission` pointing at the
existing repository, and a `Grade` where one exists. The importer never
writes to GitHub — no repos cloned, created, or modified, no webhooks
registered. See [`ghc-import.md`](ghc-import.md) Part 3 for the full table.

---

## 4. Verify the roster landed and matches

After import, the course is in Cairn's store. Check it through the running
server:

```sh
# classrooms (returns a bare JSON array)
curl -s http://localhost:8080/classrooms | python3 -m json.tool

# roster for a classroom
curl -s http://localhost:8080/classrooms/<classroom-id>/roster | python3 -m json.tool

# assignments
curl -s http://localhost:8080/assignments/<assignment-id>/submissions | python3 -m json.tool
```

Compare the roster count and usernames against your Classroom. In the
classroom this guide was tested against, the import reported 30 students, 8
assignments, and 202 submissions — and those counts matched the Classroom
exactly. A re-run reported all "existing", 0 created.

If your counts do not match, the dry-run plan (`--dry-run`) prints every row
it would create or reuse, so you can see what diverges before committing.

---

## 5. Create or confirm the first assignment

The import brings your assignment structure in from the snapshot — slugs,
deadlines, starter-code refs. New assignments are created in the dashboard
(per `deploy.md` §7), against a template repo. An imported assignment already
points at the existing org repos, so students keep working in the same place.

The one thing to confirm: deadlines are imported verbatim, but they do
**not** retroactively lock repos. Importing a course whose deadlines are all
in the past does not mass-lock 200+ repos as a side effect — the importer
pre-spends the scheduler's idempotency key so the later lock enqueue is a
no-op. If you *want* past deadlines enforced on GitHub, pass
`--retroactive-lock`; understand that it is a mass write against live repos.

---

## 6. What students do

A student joins an assignment through the self-claim flow:

1. They open the assignment's accept link — `https://<your-cairn>/assignments/<id>/accept`.
2. They authenticate through the host's OAuth (GitHub, in the default case).
   Cairn stores **only the username** — no profile data, no email.
3. The username is bound to the matching roster entry, a repo is provisioned
   (for a new assignment) or pointed at the existing one (for an imported
   assignment), and a student session starts.
4. They are redirected to `/me`, their own page.

Returning students sign in at `https://<your-cairn>/student/login`.

This flow requires a configured Git host resolver (the OAuth app whose
credentials you set in `deploy.md` §9). Without it, the accept link cannot
complete — `cairn doctor` will have warned "no Git host is configured." That
warning is the thing to resolve before students touch the system.

---

## 7. What students see

`/me` is a lightweight, framework-free page that works whether or not the
React dashboard is mounted. It lists the student's own submissions: each
shows the assignment title, classroom, a link to their repo, the deadline,
the submission status, and the latest score. An expandable section shows the
per-test breakdown and attempt history for any graded submission. If a
grading run is in progress, the page polls until it settles, then stops.

Three properties matter and are worth stating plainly, because they are the
places a migration off Classroom could go wrong:

- **A student sees only their own work.** The list and detail routes are
  scoped to the caller's own (host, username). A request for another
  student's submission returns **404** — not 403 — so the response does not
  reveal that the other submission exists.
- **The page is public; the data is not.** `/me` itself carries no data and
  renders a sign-in link to an unauthenticated visitor. The data routes
  (`/me/work`, `/me/work/{id}`) require a session and return 401 without
  one.
- **A student session cannot reach operator routes.** The student's session
  is marked non-operator; the operator middleware refuses it even if the
  username happens to match an admin. A student who is also an instructor on
  the same host does not get instructor powers through the student session.

---

## 8. How this is different from GitHub Classroom — honestly

Cairn is a control plane you run, not a service you sign up for. That is the
main difference, and it has consequences worth naming.

**What is the same.** Your org and repositories stay exactly where they are.
Students keep pushing to the same repos. Roster and assignment structure
come over from the snapshot. Deadlines and (where present) grades come over
too.

**What is different.**

- **You run it.** That means a VM, Docker, and backups. `deploy.md` makes
  this a fifteen-minute path, and `cairn doctor` tells you what is wrong, but
  it is still yours to keep running. `docker compose down` keeps data;
  `docker compose down -v` removes the database — back up with `pg_dump`
  before the second one.
- **Grades come over only where Classroom itself has them — and plenty of
  courses will have none, which is normal, not a defect.** The Classroom
  `/grades` endpoint is both incomplete (it returned 4 rows for 22 accepted
  students in one assignment) and PII-bearing (it carries student legal
  names), so Cairn does not request it. Grades are imported instead from
  `accepted_assignments[].grade`, a string like `"10/90"` — populated only by
  Classroom's own Autograding runs (GitHub Actions), and only where that
  field is non-null. The classroom this guide was tested against
  (INFO-526-SU26) simply never had grades in Classroom's system at all —
  **202 of 202 submissions, null across the board** — not a case of grades
  existing somewhere and failing to transfer. Expect this to be common, not
  an edge case: any course graded by hand, in an LMS, or with a tool other
  than the Autograder arrives in Cairn with a roster and repos and **no**
  grades, and that is an accurate migration, not a lossy one — there was
  nothing in Classroom to bring over. Imported grades, where they do exist,
  carry `graded_at` as the import time (not the original grading time, which
  the API does not expose accurately) and a `breakdown` marking their
  source, so an imported score is always distinguishable from one Cairn
  produced.

  **Optional, only if you specifically want an autograded score after the
  fact:** GitHub Classroom lets you add autograding tests to an assignment
  retroactively, even after acceptance, then trigger a run against each
  already-accepted repo (a `workflow_dispatch` or a trivial commit is enough)
  so the Autograder computes and stores a score — do this **before your
  final snapshot**, then re-run `cairn import ghc` (idempotent; it only fills
  in the previously-null rows). This computes a *new* score against whatever
  tests you add now — it does not recover whatever a TA, Gradescope, Canvas,
  or a spreadsheet originally assigned, and it is not something most
  instructors migrating a finished course will want to do. Mentioned here
  for completeness, not as the expected path.
- **Group assignments import with a known limitation.** Every team member
  gets a roster entry, but Cairn's `Submission` has a single
  `roster_entry_id`, so the team's shared repo attaches to one deterministic
  primary member rather than to all of them. The dry-run prints full team
  composition, and the import summary reports how many teams were collapsed.
  Faithful group support needs a teams table — a schema change, deliberately
  not made yet. Pass `--skip-group` to leave group assignments out entirely.
- **No webhooks are registered on imported repos.** Pushes to imported repos
  are not graded until an instructor opts in, because registering webhooks is
  a live write to GitHub, which the import does not do.
- **Grading is opt-in and runs where Cairn runs.** Enable it with
  `CAIRN_GRADER=container` (sandboxed: no network by default, dropped
  capabilities, read-only rootfs, resource limits) or
  `CAIRN_GRADER=local-exec-unsafe` (no isolation — trusted use only). Until
  it is set, grade requests are rejected with a 409 and a message telling
  you exactly what to set.

**What Cairn does not do yet.** Group assignments are collapsed as above.
Webhook-driven regrading of imported repos needs the instructor to opt in.
The React dashboard's student-facing views are follow-up; the `/me` page
this guide points students at is a standalone page, not part of the
dashboard SPA.

---

## 9. You are not locked to GitHub afterwards

Cairn's differentiator is that it is host-agnostic, not GitHub-only. The
Forgejo/Gitea and GitLab adapters both work end to end. An instructor
migrating *off* GitHub Classroom is a natural audience for "and you are not
locked to GitHub afterwards either."

If you want to move the repos themselves off GitHub — now or later — see
[`docs/migrating-github-to-forgejo.md`](migrating-github-to-forgejo.md) and
the per-host setup guides: [`github-setup.md`](github-setup.md),
[`forgejo-setup.md`](forgejo-setup.md),
[`gitlab-setup.md`](gitlab-setup.md). A classroom may declare its host, and a
single Cairn server can serve classrooms on different hosts. You can bring
Cairn up on GitHub first and add Forgejo or GitLab afterwards.

---

## Rollback — what happens if you stop halfway

Migration is not a transaction that has to complete or roll back. Each stage
is independently safe to stop at:

- **After capturing the snapshot, before deploying:** you have lost nothing.
  The snapshot is on disk; the Classroom is still live. Deploy and import
  whenever you are ready, including after September 4 (from the snapshot).
- **After deploying, before importing:** Cairn is running and empty. It does
  nothing to GitHub. `docker compose down` stops it; `docker compose down
  -v` removes its empty database.
- **After importing, before students use it:** the course is in Cairn and
  unchanged on GitHub. Students are still in Classroom (until August 28).
  You can run both in parallel, point students at Cairn when ready, and
  leave the GitHub org as a dormant backup.
- **After students are on Cairn:** the repos are still the same org repos.
  Stopping Cairn stops grading and the dashboard, but students can still
  `git push` to their repos directly — they are ordinary GitHub
  repositories. Restart Cairn and the control plane picks back up.

The only thing that is not reversible is **not capturing the snapshot before
September 4**. After that date, the roster and assignment metadata is gone
from GitHub and no rollback recovers it.

---

## One week out

If you are reading this in late August with the shutdown days away, here is
the minimum ordered sequence that gets a course running, deferring polish:

1. **Capture the snapshot of every classroom, now.** This is the one
   irreversible step and it takes a minute per classroom.
   `cairn import ghc --classroom <id> --dry-run --snapshot ./ghc-<id>`.
   Do this even if you have not deployed anything yet.
2. **Deploy Cairn** per [`deploy.md`](deploy.md) §1–§7. Skip HTTPS and
   operator login for now (the server runs in open mode on a fresh VM nobody
   knows about) — but come back to `deploy.md` §8–§9 before students touch
   it.
3. **Import** from the snapshot: `cairn import ghc --from ./ghc-<id>`.
4. **Point students at `/me`** and the accept links. The student page works
   without the dashboard.

Defer: HTTPS (until you have a domain), the React dashboard (the `/me` page
suffices), grading (until you have a container image and want autograding),
group-assignment fidelity, and webhook-driven regrading. None of those block
a course running. The snapshot is the thing that blocks, and it is the first
step.
