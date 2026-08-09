# Cairn — Instructor Console

A React + TypeScript single-page app for operating the Cairn control plane:
classrooms, assignments, roster, submission status and scores, deadlines, and the
lock / unlock / grade actions. It talks only to the control-plane HTTP API.

## Develop

The dashboard expects the Go control plane running on `:8080`:

```sh
# terminal 1 — API
cd .. && go run ./cmd/cairn            # listens on :8080 by default

# terminal 2 — dashboard
npm install
npm run dev                            # http://localhost:5173
```

In dev, Vite proxies the API path prefixes (`/classrooms`, `/assignments`,
`/auth`, `/healthz`) to the Go server, so the browser stays same-origin and no
CORS configuration is needed. Point the proxy elsewhere with `CAIRN_API_URL`.

## Test

```sh
npm test           # run the unit tests once (Vitest)
npm run test:watch # re-run on change while developing
```

Vitest runs in a `node` environment — the tested units are pure functions
(roster parsing and hashing, URL builders), so no DOM implementation is
needed; the one browser API a test touches (`window.location.origin`) is
stubbed per-test. Config lives in `vitest.config.ts`, kept separate from
`vite.config.ts` so test settings cannot affect the production build.

`src/roster-parse.test.ts` covers the privacy-critical property that a
plaintext email address never reaches the API payload, and pins the hash
vectors shared with the Go agent (`internal/rosteragent`) so the two
implementations cannot silently diverge.

There is no CI in this repo yet, so **run this before pushing** — nothing
else will.

## Build

```sh
npm run build      # type-checks (tsc --noEmit) then emits static files to dist/
npm run preview    # serve the production build locally
```

`build` deliberately does *not* run the tests: it executes inside the
production `Dockerfile`, and a failing test should stop a contributor, not a
deploy.

Serve `dist/` behind the same origin as the API (a reverse proxy, or the Go
server itself) so the app's relative API calls resolve. The simplest path is to
let the control plane serve it directly — one binary, one origin, no CORS:

```sh
CAIRN_WEB_DIR=dist go run ../cmd/cairn     # API + dashboard on :8080
```

To target a different API origin at build time, set `VITE_API_BASE`.

## Notes

- **Design**: an "engineering ledger" aesthetic — warm paper, ink, a single
  vermilion accent, IBM Plex Mono for labels/IDs/metrics with IBM Plex Sans for
  body. Styling is hand-written CSS with design tokens in `src/styles.css`; no UI
  framework.
- **Fonts** load from Google Fonts at runtime (see `index.html`). For a fully
  self-hosted, privacy-preserving deployment, vendor IBM Plex locally and remove
  the `<link>` tags.
- **Auth**: there is no operator authentication yet — the new-classroom form sends
  a placeholder `created_by`. Real instructor login is future work.
- **Scope**: this is the instructor console. Student-facing views are a follow-up.

## Layout

```
src/
  api.ts                 typed client + response types (mirrors the Go API)
  App.tsx                shell: classroom load, sidebar, new-classroom modal, toast
  styles.css             design tokens + component styles
  components/
    ui.tsx               Button, Field, StatusChip, Modal, Empty
    Sidebar.tsx          brand + classroom navigation
    ClassroomDetail.tsx  header, assignments section + create form
    AssignmentCard.tsx   metadata, deadline editor, lock/unlock/grade, submissions
    RosterPanel.tsx      roster list + add-by-username
```
