# Privacy

This document describes the **as-built** privacy posture of Cairn: what it
stores, what it deliberately does not store, and where the remaining exposure
is. Every claim cites where the behavior is implemented, so a reviewer can read
the code against the claim rather than taking this document's word for it.

Where a decision is still open, it is marked as such. Nothing here is
aspirational.

Platform-level policy — which infrastructure may hold which class of data, and
who authorizes exceptions — lives in one place:
[`educloud/docs/policy/data-destinations.md`](../educloud/docs/policy/data-destinations.md).
This document is that policy's Cairn-side pointer.

**Not legal advice.** Following the convention set in `DESIGN.md` §10.

---

## Privacy by schema

The privacy invariant is enforced by the shape of the database, not by
application logic that could be bypassed or by policy text that could be
ignored. The migration says so at the top, in the schema itself:

> `-- PRIVACY: this schema has no column for a student's legal name, SIS ID, or`
> `-- plaintext email. A student's identity anchor is their Git-host username`
> `-- (roster_entries.host_username). See DESIGN.md sections 5 and 6.`

Source: `internal/store/migrations/0001_init.up.sql`.

**What is never stored, server-side, for a student:**

- Legal name — no column exists.
- SIS ID or institutional student number — no column exists.
- Plaintext email — no column exists on any student-bearing table.

**What is stored for a student:**

| Field | What it is | Where |
|---|---|---|
| `host_username` | The Git-host username. The durable identity anchor. | `roster_entries` |
| `email_hash` | **Optional**, salted, one-way. Used only for client-side re-matching against an LMS pull. Never reversible to an address; never the plaintext. | `roster_entries` |
| `repo_namespace`, `repo_name` | The submission repository | `submissions` |
| `score`, `max_score`, `graded_at` | Autograder output | `grades` |

Source: `internal/store/models.go` — see the `RosterEntry` doc comment, which
states the constraint as a requirement on future changes: *"the
privacy-critical record: it binds a Git-host username to a classroom. It MUST
NOT carry a legal name, SIS ID, or plaintext email."*

**`users.email` is not student data.** The `users` table holds platform
operators — instructors and TAs — and storing an operator's own email is
intentional. The schema comment marks the distinction inline (`-- operator
(instructor/TA), not a student`), and `models.go` repeats it on the `User`
type: *"Storing an operator's own email is fine; the privacy constraints apply
to students."*

## The name↔username map never reaches the server

Instructors think in names; the server stores usernames. Cairn resolves that
tension client-side rather than by relaxing the schema:

> "the **name↔username map lives in the instructor's browser** (local store the
> instructor maintains, or re-derived from a fresh local pull each session).
> The dashboard renders real names *locally*; the server never sees them."

Source: `DESIGN.md` §6. The same section describes the Phase 3 roster agent,
which runs locally in the instructor's authenticated context and whose only
outputs to the server are join links and invite tokens — students then
self-claim via OAuth on the Git host, and the server persists `host_username`
plus the optional `email_hash`.

This is what makes minimization a usable feature rather than a tax, and it is
why the roster can be seeded from an LMS without the control plane ever
receiving a name.

## What this does *not* eliminate

Stated plainly, because the honest version is more useful than the flattering
one, and because `DESIGN.md` §10 already says it:

> "Storing only usernames + hashed emails **reduces** the PII surface (real
> data minimization). It does **not** eliminate FERPA: a score tied to an
> identifiable student is an education record regardless of whether we store
> their legal name."

Two facts follow:

1. **A Git-host username is an indirect identifier.** Many students choose
   usernames containing their real name, and in a small section a username plus
   course enrollment identifies a person with reasonable certainty.
2. **The `grades` table exists.** A score bound to that username, in a course
   context, is an education record.

So Cairn's database is classified **R2 (education records)** under the platform
policy, and the destination rules in `data-destinations.md` §4.1 apply to it in
full: it belongs on institutional or self-stewarded infrastructure, not on
shared or commercial infrastructure.

## Cairn is a working store, not the gradebook of record

This is the decision from which most of the others follow, and it is stated
here because it had previously been implicit.

The institution's LMS is the system of record for official grades. Cairn
provisions repositories, runs grading, and **exports** scores. Consequently:

- Student rights of inspection and correction are served from the LMS.
- Institutional retention schedules govern the LMS copy and take precedence.
- Cairn's copy of a grade is, after export, redundant risk rather than a
  record anyone depends on.

## Open items

These are gaps, not decisions. Tracked in `data-destinations.md` §8.

- **No retention or purge path exists.** The repository was searched on
  2026-07-29: there is no retention schedule, no purge, and no deletion path
  anywhere in the codebase. Grades accumulate indefinitely. The intended policy
  is purge-after-confirmed-export; the implementation is outstanding.
- **No roster-entry deletion** with dependent rows. Required to support a
  removal request even though the LMS is the record of truth.
- **`README.md`'s and the white paper's "never student records" phrasing** is
  slightly ahead of the schema while the `grades` table retains scores.
  Implementing purge-after-export makes the claim exact; until then the
  accurate phrasing is "no student names, SIS IDs, or plaintext emails."
- **Deployment destination for the Fall 2026 pilot is undecided** between
  institutional and self-stewarded hosting. This blocks the pilot and is
  `data-destinations.md` §8 item 1.

## Compliance posture

`DESIGN.md` §10 carries the strategic framing, marked there as
**[DECIDE later, with counsel]**. Its conclusion is the one this document
operates under:

> **Self-hosting** — records never leave infrastructure the institution already
> stewards. The strongest FERPA story, and another reason to lead with it.
