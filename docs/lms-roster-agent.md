# The LMS roster agent (`cairn roster pull`)

Building a Cairn roster from your LMS, without your students' names or email
addresses ever leaving your computer.

The agent reads your class roster, matches each student to a Git-host username
**on your machine**, and sends Cairn only `{username, email_hash}` — which is
exactly what `store.RosterEntry` is allowed to hold: it "MUST NOT carry a legal
name, SIS ID, or plaintext email."

---

## Brightspace: what access actually requires

**Verified against D2L's current developer documentation on 2026-08-09** — not
assumed, per this repo's platform invariant.

**An instructor cannot self-issue a Brightspace API token.** There is no
equivalent of Canvas's personal access token, which any user can mint from their
own settings page. Specifically:

- Applications are registered through the **Manage Extensibility** admin tool.
  D2L's docs state plainly: "You use the Manage Extensibility admin tool to
  register your application."
- Doing so requires the **"Can Manage API Applications"** permission. Users with
  it "will see the ID Key Authorization and OAuth 2.0 tabs in Manage
  Extensibility, as well as the Register an App buttons within those tabs" — an
  administrator capability, not one an instructor role carries by default.
- The newer OAuth 2 **Client Credentials** flow is gated further still: "a
  Brightspace administration must prepare your service user and role ahead of
  time, with only the permissions and enrollments required for your
  application."

This is a genuinely different trust model from Canvas, and it is why this agent
does not treat the two as symmetric. For Brightspace, **the API is the path that
needs institutional approval, and the export is the path that works today.**

### The three paths, in the order most instructors will use them

| Path | Needs approval? | Use it when |
|---|---|---|
| `--export classlist.csv` | **No** | The default. You can already see your classlist. |
| `--base-url/--org-unit/--token` | **Yes — admin-registered app** | Your institution has already granted API access. |
| Manual bulk add (dashboard) | **No** | No export, or no connector for your LMS. |

All three converge on the same endpoint and the same hashing, so a roster
assembled partly by hand and partly by the agent stays consistent.

---

## Using it

### 1. Get a classlist export

Course → **Classlist** → Enrolled Users → export/print view → save as CSV.

Column names vary by institution and locale, so the agent matches headers by
meaning: a `Name` column, or `First Name`/`Last Name`; optionally `Email` and
one of `Username`/`OrgDefinedId`. Extra columns are ignored rather than
rejected — real exports carry many.

### 2. Collect the Git usernames

Matching happens locally, so the agent needs to know which usernames are
possible. One per line, optionally with a full name to match against:

```
# usernames.txt
jdoe,Jane Doe
bsmith
carol-m,Carol Mendez
```

### 3. Dry-run, review, then send

```sh
cairn roster pull --lms brightspace --export classlist.csv \
  --candidates usernames.txt --classroom <id> --dry-run
```

Nothing is sent under `--dry-run`; it prints the exact request body. Drop the
flag (and add `--server https://your-cairn`) to submit.

### If your institution HAS granted API access

```sh
cairn roster pull --lms brightspace \
  --base-url https://d2l.example.edu --org-unit 12345 \
  --token "$BRIGHTSPACE_TOKEN" --classroom <id>
```

This calls `GET /d2l/api/le/{version}/{orgUnitId}/classlist/`. Note that
Brightspace can suppress `Email`, `FirstName`, `LastName` and `Username`
per org-unit configuration, so the agent treats every field as optional.

---

## How matching works, and where it asks

Three tiers, because a wrong roster match sends a student to the wrong repo:

- **exact** — the LMS name equals a candidate's full name, or the email's
  local-part equals a username. Accepted automatically.
- **needs_confirm** — one plausible candidate: a reordered `Last, First`, a
  dropped middle name, `jane.doe` vs `janedoe`. **You are asked.** `--yes`
  accepts these in bulk; `--dry-run` shows them without prompting.
- **unmatched** — no candidate, or several. **Never guessed.** Listed so you can
  add them by hand.

Anyone not matched is reported and the run is labelled **PARTIAL**. The agent
never quietly ships an incomplete roster.

---

## What leaves your machine

Only this:

```json
{"entries": [{"username": "jdoe", "email_hash": "e38449b4…"}]}
```

The hash is `sha256(classroomID + ":" + lowercase(email))`. Salting with the
classroom ID means the same address in two courses produces different digests,
so a digest cannot act as a cross-classroom identifier for a student.

That formula is byte-identical to the dashboard's client-side hashing in
`web/src/roster-parse.ts`, and a test pins the shared vectors — if the two ever
diverge, `TestHashEmailMatchesDashboardFormula` fails.

Matching and hashing perform **no network I/O**; a test enforces this by failing
on any dial attempt during either.

---

## Auditability

Every run prints what it read and what it decided — the connector used, how many
students were fetched, each name with its matched username, the match tier, and
the reason. `--audit <file>` saves it.

The log holds names and usernames (which you already have) and **never** an
access token or a plaintext email address.

---

## When it doesn't work

The agent is required to fail loudly and point somewhere useful. An unreadable
export, an unrecognized file, a refused token, or an LMS with no connector all
produce an actionable message ending at the same fallback: **dashboard →
classroom → Roster → "Bulk add"**, which needs no LMS access at all.

Canvas and Moodle connectors are not built yet — Brightspace is the pilot's LMS
and was built first. Asking for one today names the manual path rather than
failing silently.
