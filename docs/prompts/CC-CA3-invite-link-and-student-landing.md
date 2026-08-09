# CC-CA3 — Copy invite link, and stop showing signed-in students the instructor login screen

*Claude Code prompt. Authored in Cowork, 2026-08-09, from a direct instructor/
student UX review requested by Greg ("make both sides as clean and easy to use
as possible — what's missing?"). Both findings below were confirmed by reading
the actual code, not inferred from docs.

**Finding A.** `web/src/components/AssignmentCard.tsx` has no way to get the
student join link. An instructor can Grade/Lock/Unlock an assignment from the
card, but the URL a student needs — `/assignments/{id}/accept` — is never
surfaced anywhere in the UI. The instructor has to know the route exists and
hand-build it from the assignment ID. GitHub Classroom's entire roster-join UX
is one copyable invite link; Cairn has the same route already
(`server.go:155`, `GET /assignments/{id}/accept`, registered "student entry")
but nothing to copy it from.

**Finding B, more serious.** `GET /` always serves the instructor SPA
(`server.go:185`, `staticSPAHandler`). The SPA calls `GET /auth/me`
(`handleAuthMe`, `server.go:343`), which checks `operatorFromCookie` — the
admin allowlist. A student's session cookie (`cairn_session`, shared across
operator and student identity per `currentIdentity`, `server.go:285`) is
perfectly valid for `/me` and `/me/work`, but is **not** an operator, so
`/auth/me` 401s and `App.tsx` renders `operator === null`: "Instructor
console — Sign in with your Git host account to continue." **A student who is
already signed in and simply navigates to the site's base URL is told to sign
in again, on a screen built for instructors, with no link back to their own
page.** This is the actual missing "front door" — not a cosmetic gap, a
confirmed dead end in the current code path.

Both fixes are small and independent of CC-W1 (LTI). LTI will eventually give
instructors a one-click launch from inside a course LMS, but it does not ship
soon, and neither of these two things should wait for it.*

---

## 1. Read first

- `web/src/components/AssignmentCard.tsx` — where the invite-link action goes;
  match its existing `notify`/toast pattern (see `run()`'s use of `notify`)
- `web/src/api.ts` — add a small helper alongside `gradesCsvUrl` (same idiom:
  a URL-building function, no new request type needed since this is just a
  copy-to-clipboard action, not a fetch)
- `web/src/App.tsx` — the `operator === null` branch that renders the
  instructor login card; this is what a signed-in student currently hits
- `internal/api/server.go` — `handleAuthMe` (343), `currentIdentity` (285),
  `sessionFromCookie`/`operatorFromCookie`, and the route table around line
  151-162 (`/auth/login`, `/auth/me`, `/assignments/{id}/accept`,
  `/student/login`, `/me`)
- `internal/api/student.go`, `studentpage.go` — confirm what `/me` needs to
  render correctly when reached this way (it already handles being loaded
  directly; this prompt is only about how someone *arrives* there)

## 2. Fix A — copy invite link

Add a "Copy invite link" button to `AssignmentCard.tsx`, next to
Grade/Lock/Unlock, that copies `${window.location.origin}/assignments/{assignment.id}/accept`
to the clipboard and confirms via the existing `notify` toast (e.g. "Invite
link copied"). Use the browser clipboard API already available in this
Vite/React app; no new dependency.

If the classroom's `join_policy` is `roster` (see `store.ClassroomJoinPolicyRoster`,
`server.go` ~878), a student who isn't on the roster will be rejected at
accept-time — the link still works, it just requires the roster step first.
A one-line note near the button ("students must be on the roster first" /
"open to anyone with the link", switched on `join_policy`) is worth adding if
`join_policy` is already exposed on the `Classroom` type the frontend receives;
if it currently isn't (check `web/src/api.ts`'s `Classroom` interface — it
looks absent right now), add it rather than guessing client-side. Small,
in scope; don't expand this into a roster-management redesign.

## 3. Fix B — a signed-in student never sees the instructor login screen

Pick whichever of these is the smaller real change once you're in the code —
state which you chose and why:

- **Server-side redirect**: before `staticSPAHandler` serves `/`, check for a
  valid session via `currentIdentity`/`sessionFromCookie`. If there's a valid
  student-shaped session (has identity, is not an operator), 302 to `/me`
  instead of serving the SPA. Operators and unauthenticated visitors fall
  through to the SPA unchanged.
- **Client-side fix**: in `App.tsx`, when `/auth/me` 401s, don't assume "no
  one is signed in" — attempt a lightweight check (e.g. `/me/work` or a new
  cheap identity-only endpoint) before deciding to show the instructor login
  card, and redirect to `/me` if that succeeds.

The server-side redirect is probably simpler and keeps the SPA's own state
machine untouched — prefer it unless something about session/cookie handling
makes the client-side path clearly cheaper. Either way, the fix must not
change behavior for actual instructors or unauthenticated visitors: an
operator with a valid operator session still sees the dashboard; someone with
no session still sees the sign-in card. Add a link from the sign-in card to
`/student/login` regardless ("Returning student? Sign in here") as a
belt-and-suspenders fix for the case the redirect doesn't catch (e.g. cookie
present but session already expired server-side).

## 4. Tests

- A request to `/` with a valid student session redirects to `/me` (or the
  chosen equivalent), not the SPA shell.
- A request to `/` with a valid operator session is unaffected (still serves
  the SPA; still resolves via `/auth/me`).
- A request to `/` with no session is unaffected (still serves the SPA;
  `operator === null` path still renders).
- Frontend: the invite-link button copies the correct URL for a given
  assignment ID (a rendering/unit test is enough — don't test the OS
  clipboard itself).

## 5. Report

- Which option you took for Fix B and why.
- Confirm the three unaffected/affected cases in §4 all hold — this is the
  part most likely to have a subtle regression (don't want to break operator
  login while fixing the student case).
- Whether `join_policy` needed adding to the frontend `Classroom` type, and if
  so, where else in the UI it's now visible.
- `go test ./...`, `go vet ./...`, and the frontend's existing lint/test
  command (check `web/package.json`) all green.
