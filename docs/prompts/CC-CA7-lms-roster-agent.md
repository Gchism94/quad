# CC-CA7 — The LMS-roster agent: local, auditable, and honest about what it can't reach

*Claude Code prompt. Authored in Cowork, 2026-08-09, from Greg's direction:
"LMS-roster agent for the pilot would be best, but have a manual version as
well in case LMS doesn't work with it. Classroom, for instance, didn't have
Brightspace." This is Phase 3 from `ROADMAP.md`: "Open, auditable local
agent (browser extension / CLI); instructor-token API pull
(Canvas/Moodle/Brightspace) with DOM-scrape fallback; local-only
name↔username matching, server receives username (+ email hash) only."

**Depends on CC-CA6** (bulk manual roster entry) — this agent's output feeds
that endpoint. Land CA6 first; this prompt assumes
`POST /classrooms/{id}/roster/bulk` already exists.

What's already verified by reading the code: `store.RosterEntry.EmailHash`
already exists with a doc comment stating it's "an OPTIONAL salted, one-way
hash used only for client-side re-matching against an LMS pull" — the data
model was already designed for exactly this feature; nothing about the
server side needs to change to accept this agent's output.

What this prompt needs to resolve, and must not assume: **whether each
target LMS actually offers a self-serve instructor API token, or whether
that requires institutional/IT approval.** Canvas's personal-access-token
model is well documented and typically self-serve. Brightspace's equivalent
(the Valence API) has a materially different trust model — verify this
against Brightspace's current developer documentation before writing a line
of connector code, not from memory or a general impression that "LMSs have
APIs." This is the same platform invariant CLAUDE.md states: don't trust a
vendor's claimed capability, verify against the live docs/service. If
Brightspace's API isn't realistically reachable by an instructor without a
separate institutional approval process, DOM-scrape is not a fallback for
Brightspace — it is the primary path, and the prompt output should say that
plainly rather than treating both LMSs as symmetric.

**Confirmed 2026-08-09: the actual pilot course runs on Brightspace.** Build
the Brightspace connector first, not Canvas — Canvas was the easy
illustrative example, Brightspace is the one that has to work for this
pilot. Still do the §1 research rather than assuming DOM-scrape is required
— Brightspace's Valence API access model may have changed, or the
institution may already have an app-registration path — but budget for
DOM-scrape being the real answer, since that is the likelier outcome per
the trust-model difference from Canvas, and design the connector interface
so that isn't a late surprise.*

## 1. Read first

- `ROADMAP.md`'s Phase 3 section — the three-bullet scope this prompt
  implements
- `internal/store/models.go` — `RosterEntry.EmailHash`'s doc comment (the
  contract this agent's output must satisfy: local hashing, never plaintext
  email to the server)
- `docs/prompts/CC-CA6-bulk-roster-entry.md` — the endpoint this agent's
  output is sent to (`POST /classrooms/{id}/roster/bulk`); match its request
  shape exactly
- Canvas's and Brightspace's current API documentation (fetch it — don't
  rely on training data, both platforms' offerings change) — specifically
  whether an instructor can self-issue a read-scoped roster API token
  without an admin

## 2. Decide the artifact shape before building both variants

Recommend starting with a **CLI tool** (simpler distribution, no browser
extension store review cycle, works for any LMS with a real API token path)
and reserving a **browser-extension DOM-scrape mode** specifically for LMSs
where §1's research shows no self-serve API token exists. Don't build a
browser extension speculatively for an LMS that turns out to have a fine
API path — that's wasted surface area and more code review burden for
something handling roster data.

The pilot course runs on **Brightspace** — build that connector for this
prompt. Canvas and Moodle are the long-run ROADMAP target (all three named
there) but building either now, before Brightspace works, is waste against
what this pilot actually needs.

## 3. Connector interface

A small pluggable interface, one implementation per LMS:

```
FetchRoster(ctx) ([]RosterRow, error)
// RosterRow: { Name string; LMSUserID string; Email string }
```

Each connector is either token-based (calls the LMS's real roster API) or
scrape-based (parses the authenticated instructor's own roster page DOM,
run locally in a browser context the instructor is already logged into —
never store or transmit the instructor's LMS credentials). Whichever mode a
given LMS ends up using per §1's research, implement that one first; don't
build both modes for the same LMS unless the research genuinely calls for
a fallback within that one platform too.

## 4. Local-only matching, then hand off to CC-CA6

- Name → Git username matching happens **entirely on the machine running the
  agent** — this is the "ephemeral, local, auditable" design constraint from
  ROADMAP, not a preference. The agent should ask the instructor to confirm
  or correct matches it's unsure of (e.g. name variants, no exact match)
  rather than silently guessing.
- Hash each matched student's email locally (salted, one-way — match
  whatever hashing approach CC-CA6 settled on for its client-side hashing
  path, so the two prompts produce compatible `email_hash` values) before
  anything leaves the instructor's machine.
- Submit the final `{username, email_hash}` list to CC-CA6's
  `/classrooms/{id}/roster/bulk` endpoint. The agent is a *producer* for
  that endpoint, not a separate ingestion path — don't build a second
  server-side entry point for this.

## 5. Auditability and honest failure

- Log what the agent fetched and matched, in a form the instructor (or
  Greg) can inspect — this is handling FERPA-adjacent data before it ever
  reaches Cairn's server, so "trust me" isn't sufficient; "here's exactly
  what I read and matched" is the bar.
- If no connector exists yet for the instructor's LMS, or the live API/DOM
  fetch fails, say so plainly and point at **CC-CA6's bulk manual entry** as
  the immediate path — never fail silently or produce a partial roster
  without saying it's partial.

## 6. Tests

- Connector(s) built: a fake/recorded API response (or saved DOM fixture for
  scrape mode) produces the expected `RosterRow` list.
- Matching: a case with an exact match, a near-miss requiring confirmation,
  and a no-match all produce the right agent behavior (auto-accept,
  ask-to-confirm, flag-as-unmatched).
- The final output payload matches CC-CA6's bulk endpoint's expected
  request shape exactly (an integration test against a local Cairn instance
  if feasible).

## 7. Report

- Which LMS(s) were verified against live/current documentation, and what
  each one's actual instructor-token-access model turned out to be (don't
  just report "checked" — report what was found, including if Brightspace's
  model requires institutional approval rather than being self-serve).
- Which mode (token vs. DOM-scrape) was built for each, and why.
- Confirm the local-only matching and hashing claims are actually true in
  the implementation (no network call between fetching the roster and
  hashing emails).
- What happens today when a target LMS has no connector yet — confirm it
  points to CC-CA6, not a dead end.
