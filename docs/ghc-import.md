# Importing from GitHub Classroom

GitHub Classroom is being retired. Sign-ups are already disabled, the service shuts
down **August 28, 2026**, and classroom-specific data is **permanently deleted
September 4, 2026**. Accounts, organizations, and repositories are explicitly
unaffected and persist indefinitely.

That split is the whole basis of Cairn's import path:

- **What dies** is the *metadata* — which classroom an assignment belonged to, which
  student accepted it, which repo is theirs, what they scored.
- **What survives** is the *substance* — the org and every student repository in it,
  as ordinary repos.

So `cairn import ghc` reads the perishable metadata while it still exists, writes it
into Cairn's own store, and **points at the existing repositories rather than
cloning or recreating them**. The importer never writes to GitHub at all.

---

## Part 1 — Findings: what the export surface actually is

Constraint 4 of this repo's operating rules says to treat GitHub Classroom's export
surface as unverified until inspected against a real classroom. This section is that
inspection, performed **2026-08-05** against the maintainer's own account: 30 real
classrooms, examined principally through `INFO-523-S26` (classroom `298020`, org
`INFO-523-S26`, 258 repositories).

Every claim below was checked by request. Where a claim contradicts the desk
research that preceded it, the observation wins and the contradiction is called out.

### The Classroom REST API is live, complete, and sufficient

The desk research proposed building against
`github-education-resources/classroom-export-utility`, a shell script wrapping the
`gh classroom` CLI extension. **That indirection is unnecessary.** Both the script
and the extension are thin clients over a plain REST API that is reachable
directly, and the API returns everything the importer needs.

A **user** token with the `read:org` scope reaches all of it. (Verified with a token
carrying `gist, read:org, repo, workflow`.)

| Endpoint | What it provides |
|---|---|
| `GET /classrooms` | `id`, `name`, `archived`, `url` |
| `GET /classrooms/:id` | the above plus `organization.login` — **the org slug** |
| `GET /classrooms/:id/assignments` | `id`, `title`, `slug`, `type`, `deadline`, `accepted`, `submissions`, `passing` |
| `GET /assignments/:id` | the above plus `starter_code_repository{full_name, default_branch}` |
| `GET /assignments/:id/accepted_assignments` | `students[].login`, `repository.full_name`, `grade`, `submitted`, `commit_count` |

Cairn talks to these endpoints with `net/http` and `encoding/json`. It does not
shell out to `gh`, and it adds no dependency.

> **The App token does not work here.** Cairn's GitHub adapter authenticates as a
> GitHub App installation (`CAIRN_GITHUB_APP_ID` etc.). The Classroom API is
> **user-scoped** — an installation token cannot read `/classrooms`. The importer
> therefore takes a separate user token; see [Part 2](#part-2--using-it).

### `accepted_assignments` is the authoritative source — and the only one needed

One object carries the student login and the repository together, so no inference
is required to connect them.

Its row count matched the assignment's own `accepted` count exactly, every time:

| Assignment | `accepted` | rows returned |
|---|---|---|
| `hw-01` (911066) | 22 | 22 |
| `final-project` (911089) | 19 | 19 |
| `ex-09` (913081) | 19 | 19 |
| `final-project` group (886905) | 17 | 17 |

### `GET /assignments/:id/grades` is rejected as an input

The desk research expected `grades.csv` to be a primary input. It is not used, for
two independent reasons — either would be sufficient on its own.

**1. It is incomplete.** For `hw-01`, the grades endpoint returned **4 rows for 22
accepted students**. It appears to return only students in some submitted/passing
state, which is not a roster. Building a roster from it would silently drop 18 of 22
students.

**2. It carries student PII.** Its `roster_identifier` field is a student **legal
name** (verified by shape only — 4/4 values containing a space, no `@`, no digits;
the values themselves were never printed or stored). `internal/store/models.go`
states the invariant plainly: a `RosterEntry` "MUST NOT carry a legal name, SIS ID,
or plaintext email." Reading a legal name in order to discard it is still reading
it, and it would land in any captured snapshot on disk. Cairn does not request this
endpoint at all.

**And it is the less accurate source anyway.** `points_available` was the string
`"0"` on *every* row of *every* assignment in that classroom — while
`accepted_assignments[].grade` for the same assignment reads `"10/90"`. The
PII-free source has the correct denominator; the PII-bearing one does not.

This is a happy result rather than a compromise: the field Cairn must not read is
also the field it has no reason to want.

### Repository naming

- **Individual assignments:** `<assignment.slug>-<username>`. Verified 22/22 for
  `hw-01`, whose repos are `hw-01-<login>` in the org.
- **Group assignments:** `<assignment.slug>-<team-name>`, where the team name is
  **chosen by students** — e.g. `final-project-<first>-<last>-solo`. This is **not
  derivable** from any roster.

The desk research's fallback plan — infer the naming convention by pattern-matching
the org — is therefore retired. It cannot work for group assignments, and it is
unnecessary for individual ones, because `repository.full_name` is already in the
data. Cairn uses the recorded repository name and never reconstructs one.

### Starter code

`GET /assignments/:id` returns `starter_code_repository` with `full_name` and
`default_branch`, which maps directly onto Cairn's `adapter.TemplateRef`. Note the
starter repo is often private and named verbosely, e.g.
`INFO-523-S26/info-523-s26-classroom-python-numpy-and-pandas-foundations-hw-01`.

### Group assignments exist and are a real case

They are rarer than individual ones but present: of six recent classrooms surveyed,
one (`283732`) had a group assignment (`final-project`, `max_members: 2`, 17
accepted). Its `accepted_assignments[].students` array holds every member.

### Confirmation that repos outlive the shutdown

`GET /orgs/INFO-523-S26/repos` returned **258 repositories**, including all 22
`hw-01-*` repos, as ordinary org repos with no Classroom involvement. Combined with
GitHub's retirement notice — which states that accounts, repositories, and
organizations are unaffected — the working hypothesis is confirmed. Import binds to
these repos in place.

### Sources

- [GitHub Classroom retirement FAQ](https://github.com/orgs/community/discussions/145312)
- [Classroom sign-ups are no longer available](https://github.blog/changelog/2026-05-26-github-classroom-sign-ups-are-no-longer-available/)
- [Export or migrate GitHub Classroom data](https://docs.github.com/en/education/manage-coursework-with-github-classroom/get-started-with-github-classroom/export-or-migrate-github-classroom-data)
- [classroom-export-utility](https://github.com/github-education-resources/classroom-export-utility) — evaluated, not used

---

## Part 2 — Using it

### Get a token

The Classroom API needs a **user** token (the GitHub App installation token Cairn
uses elsewhere will not work). Either reuse the `gh` CLI's:

```sh
export CAIRN_GHC_TOKEN="$(gh auth token)"
```

or create a classic PAT with the `read:org` scope. The importer also accepts
`GITHUB_TOKEN` / `GH_TOKEN`, or `--token`.

### Find the classroom

```sh
cairn import ghc --list
```

### Dry run first

```sh
cairn import ghc --classroom 298020 --dry-run --snapshot ./ghc-298020
```

This prints the complete plan — every classroom, assignment, roster entry,
submission, and grade it would create or reuse — and **writes nothing to the Cairn
store**. It does capture the snapshot, which is the point of running it early.

### Import

```sh
cairn import ghc --classroom 298020 --snapshot ./ghc-298020
```

The import is **idempotent**: re-running it creates nothing new. Rows are matched on
their natural keys (classroom by host + org, assignment by slug, roster entry by
username, submission by assignment + roster entry), so a re-run after a partial
failure resumes safely.

### Snapshots outlive the API

Every live run writes a snapshot directory of the raw JSON. After September 4 the
API is gone, but a snapshot still imports:

```sh
cairn import ghc --from ./ghc-298020
```

**Take a snapshot of every classroom you may ever want, before August 28.** It is a
handful of read-only requests and it is the only copy of that metadata that will
exist. The snapshot contains GitHub usernames and repository names; it contains no
legal names, because the endpoint carrying them is never requested. `ghc-snapshot-*/`
is gitignored so a capture taken in the repo is not committed by accident.

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `--classroom <id>` | — | Classroom to import (omit with `--from`) |
| `--from <dir>` | — | Import from a snapshot instead of the API |
| `--snapshot <dir>` | `./ghc-snapshot-<id>` | Where to write the captured JSON |
| `--dry-run` | off | Print the plan; change nothing |
| `--list` | off | List classrooms and exit |
| `--org <slug>` | from API | Assert/override the org slug |
| `--created-by <user>` | the token's login | Operator attribution on created rows. With `--from` there is no token to ask, so pass it explicitly or the import is unattributed |
| `--join-policy` | `roster` | `roster` or `open` on the created classroom |
| `--no-grades` | off | Skip importing historical grades |
| `--skip-group` | off | Skip group assignments entirely |
| `--retroactive-lock` | off | Allow past deadlines to lock repos — see below |
| `--token <tok>` | env | Classroom API token |

---

## Part 3 — What gets imported, exactly

| Cairn row | Source | Notes |
|---|---|---|
| `Classroom` | classroom `name` + `organization.login` | `host: github`, `join_policy: roster` |
| `Assignment` | assignment `title`, `slug`, `type`, `deadline` | `TemplateRef` from `starter_code_repository`; `GradingSpec` empty |
| `RosterEntry` | `students[].login` | status `active`; **username only** |
| `Submission` | `repository.full_name` | status `active`; points at the existing repo |
| `Grade` | `accepted_assignments[].grade` | only when non-null |

### Deadlines are imported as-is, but do not retroactively lock repos

Cairn's scheduler (`internal/provisioning/scheduler.go`) enqueues a lock job for
every submission of every assignment whose deadline has passed. Importing a course
whose deadlines are all in the past would therefore make the running server lock
**every imported repo** — around 250 of them for a single semester — as a side
effect of an import that is supposed to be read-only toward GitHub.

Deadlines are still imported verbatim, because they are real course data. The lock
storm is prevented by *pre-spending the scheduler's own idempotency key*: for each
submission under an already-past deadline, the importer records a completed
provisioning job with key `lock:<submissionID>`. The scheduler's later enqueue is
then a no-op. This is the same mechanism the scheduler already documents for an
instructor who unlocks a repo for an extension — "that key is already spent" — not a
new one.

If you *want* the historical deadlines enforced on GitHub, pass
`--retroactive-lock`. Understand that it is a mass write against live repositories.

### Grades are imported with their provenance, and their limits

Scores come from `accepted_assignments[].grade`, a string like `"10/90"` → score 10,
max 90. Rows where it is null are skipped and counted in the summary.

Two honest caveats, recorded on every imported grade:

- **`graded_at` is the import time, not the original grading time.** The only
  submission timestamps GitHub exposes live on the rejected `/grades` endpoint — and
  even there, 17 of 19 rows for `ex-09` had a blank timestamp. There is no accurate
  value to import, so Cairn does not invent one.
- Each grade's `breakdown` records `{"source":"github-classroom","raw":"10/90"}`, so
  an imported score is always distinguishable from one Cairn itself produced.

Pass `--no-grades` to skip them.

### Group assignments are imported with a known limitation

Every team member gets a roster entry. But Cairn's `Submission` has a single
`roster_entry_id`, and repo→submission lookup assumes one submission per repo, so
the team's shared repo is attached to **one** deterministic primary member (first
login, sorted) rather than to all of them.

Nothing is hidden: the dry-run prints full team composition for every group repo,
and the import summary reports how many teams were collapsed this way. Faithful
group support needs a teams table — a schema change, deliberately not made here.

Pass `--skip-group` to leave group assignments out entirely.

### What is deliberately not done

- **No webhooks are registered** on imported repos, so pushes to them are not graded
  until an instructor opts in. Registering webhooks is a live write to GitHub, which
  the importer does not do.
- **No repos are cloned, created, or modified.** The org outlives the shutdown; the
  repos stay exactly where they are.
- **Group repository names may embed student legal names** (`final-project-<first>-<last>-solo`),
  because students chose those names on GitHub. Cairn must store the name to address
  the repo. This is an exposure inherited from GitHub, not one Cairn creates, and it
  is the one place where importing cannot fully honor the roster privacy invariant.
