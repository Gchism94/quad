# Quad

> Settled name: **Cairn** (see `../educloud/SYSTEM.md`). The repo and code
> keep the working name until the publish-step rename, applied in one commit.

**An open-source, host-agnostic, privacy-minimal platform for distributing and
auto-grading coding assignments backed by Git.** The GitHub Classroom workflow,
rebuilt so that educators own their data and aren't locked to a single vendor.

> **Status: pilot sprint (July 2026).** Phase 1 is complete and the GitHub,
> Forgejo, and GitLab adapters work end to end. Remaining before the Fall
> pilot: Classroom import, `doctor`, the one-command deploy path, and student
> views. See [`ROADMAP.md`](ROADMAP.md) and [`KICKOFF-PROMPT.md`](KICKOFF-PROMPT.md).

## Why

GitHub Classroom is being decommissioned (full shutdown **August 28, 2026**).
The successors split into thin **GitHub-only** clones and **closed, hosted**
products. Quad targets the gap none of them fill:

- **Host-agnostic.** GitHub, GitLab, and self-hosted **Forgejo/Gitea** sit behind
  one adapter interface. The Git host is a plugin, not a foundation.
- **Privacy by architecture.** The server stores Git usernames — *not* student
  names, SIS IDs, or plaintext emails. (See [`DESIGN.md`](DESIGN.md) §5–6.)
- **Self-hostable first.** One binary, SQLite by default (Postgres optional).
  Records stay on the institution's own infrastructure.
- **Portable autograding.** A host-neutral grading spec runs in Quad's own
  sandboxed runners — not locked to GitHub Actions.
- **No lock-in.** CSV roster import/export, a documented schema, an open adapter
  SDK, and gradebook-friendly exports.

## Host support

| Host | Status | Notes |
|---|---|---|
| **GitHub** | ✅ Supported | GitHub App + OAuth — the familiar on-ramp. See [`docs/github-setup.md`](docs/github-setup.md). |
| **Forgejo** | ✅ Supported | Self-hosted; token + OAuth2. See [`docs/forgejo-setup.md`](docs/forgejo-setup.md). |
| **Gitea** | ✅ Supported | Same adapter family as Forgejo (shared `/api/v1`); declare `host: gitea`. |
| **GitLab** | ✅ Supported | gitlab.com or self-hosted; PAT + OAuth2 (`/api/v4`). See [`docs/gitlab-setup.md`](docs/gitlab-setup.md). |

A single deployment can serve classrooms on multiple hosts at once; each classroom
carries its own `host`. Moving courses from GitHub to a self-hosted instance is a
config change, not a rewrite — see [`docs/migrating-github-to-forgejo.md`](docs/migrating-github-to-forgejo.md).

## Layout

```
cmd/quad/            control-plane entrypoint
internal/            AGPL-3.0 server internals
  api/               HTTP server + routes
  store/             domain models + SQL migrations  (privacy-critical schema)
  provisioning/      durable, idempotent, rate-limited job queue
  config/            runtime config
pkg/                 Apache-2.0 reusable primitives
  adapter/           THE host-adapter interface + the GitHub adapter
  gradingspec/       the portable grading-spec schema
web/                 React/TS instructor dashboard (Vite) — see web/README.md
docs/                operator guides (forgejo-setup.md, …)
.env.example         all QUAD_* environment variables, commented
DESIGN.md            the design doc / rationale
ROADMAP.md           phased plan
```

## Quick start (local, zero config)

```sh
go run ./cmd/quad
# → store: sqlite /absolute/path/to/quad.db
# → quad control plane listening on :8080
curl http://localhost:8080/healthz   # {"ok":true}
curl http://localhost:8080/          # status page
```

No database daemon, no build tags, no configuration required. Quad creates
`quad.db` in the working directory on first run and keeps it across restarts.

From here, pick a host to back real classrooms:

- **GitHub** — zero new infrastructure, familiar to GitHub Classroom users:
  [`docs/github-setup.md`](docs/github-setup.md)
- **Forgejo / Gitea** — self-hosted, data stays on your infrastructure:
  [`docs/forgejo-setup.md`](docs/forgejo-setup.md)
- **GitLab** — gitlab.com or self-hosted:
  [`docs/gitlab-setup.md`](docs/gitlab-setup.md)
- **Moving GitHub → self-hosted** later, without rebuilding materials:
  [`docs/migrating-github-to-forgejo.md`](docs/migrating-github-to-forgejo.md)

## Build

Requires Go 1.25+.

```sh
go build ./...      # compiles the whole tree
go run ./cmd/quad   # starts the control plane on :8080
```

### Dashboard

The instructor console lives in `web/` (Vite + React + TypeScript). For
development, run the API and the Vite dev server side by side (see
`web/README.md`). To ship a **single binary** that serves both the API and the
UI, build the dashboard and point the server at the output:

```sh
(cd web && npm install && npm run build)   # emits web/dist
QUAD_WEB_DIR=web/dist go run ./cmd/quad     # API + dashboard on :8080
```

Specific API routes always take precedence; everything else is served from
`QUAD_WEB_DIR` with SPA fallback.

## Persistence

Store selection — in priority order:

| Condition | Store used |
|---|---|
| `QUAD_STORE=memory` | In-memory (ephemeral — lost on restart; useful for tests) |
| `QUAD_STORE=sqlite` | SQLite at `QUAD_SQLITE_PATH` (default: `quad.db`) |
| `QUAD_STORE=postgres` | PostgreSQL via `QUAD_DATABASE_URL` |
| `QUAD_DATABASE_URL` set | PostgreSQL (auto-detected) |
| _(nothing set)_ | **SQLite `quad.db`** — zero-config default |

### SQLite (default)

```sh
go run ./cmd/quad                       # creates quad.db in the working dir
QUAD_SQLITE_PATH=/var/lib/quad/quad.db go run ./cmd/quad   # explicit path
```

No daemon, no migrations to run manually — the schema is applied automatically on
first open. WAL mode is enabled so reads don't block writes.

### PostgreSQL (scale-out)

```sh
export QUAD_DATABASE_URL=postgres://quad:quad@localhost:5432/quad?sslmode=disable
QUAD_DB_AUTOMIGRATE=1 go run ./cmd/quad
```

`QUAD_DB_AUTOMIGRATE=1` applies the embedded schema on startup (idempotent).
Omit it to manage migrations yourself from `internal/store/migrations/`.
Integration tests against a live database:

```sh
QUAD_TEST_DATABASE_URL=postgres://quad:quad@localhost:5432/quad?sslmode=disable \
  go test ./internal/store/postgres
```

## Authentication

Operator authentication protects the management API and dashboard. Operators sign
in with their Git-host account (reusing the same OAuth app as the student claim
flow), and only allowlisted usernames may operate the instance. Sessions are
HttpOnly cookies (kept in memory, so a restart signs operators out).

Enable it by setting the allowlist and configuring operator OAuth:

```sh
export QUAD_ADMIN_USERS=alice,bob                       # allowlisted operator usernames
export QUAD_GITHUB_CLIENT_ID=...                        # OAuth app (shared with student flow)
export QUAD_GITHUB_CLIENT_SECRET=...
export QUAD_OAUTH_REDIRECT_URL=https://your-host/auth/callback
export QUAD_COOKIE_SECURE=1                             # set behind HTTPS
QUAD_WEB_DIR=web/dist go run ./cmd/quad
```

When `QUAD_ADMIN_USERS` is unset the server runs **open** (no auth) and logs a
warning — fine for local development, not for anything exposed. `QUAD_AUTH_DISABLED=1`
forces open mode even if an allowlist is present.

Because auth uses same-origin cookies, exercise the login flow against the
single-binary build (`QUAD_WEB_DIR=web/dist`), not the split Vite dev server; for
day-to-day UI development, leave auth disabled.

### Required GitHub App permissions

> Full GitHub walkthrough (App creation, OAuth, validation, troubleshooting):
> [`docs/github-setup.md`](docs/github-setup.md).

The GitHub App backing the `QUAD_GITHUB_APP_ID` installation needs these
**repository-level** permissions:

| Permission | Level | Why |
|---|---|---|
| **Administration** | Read & write | Repo creation (`POST /repos/:tmpl/generate`) and `LockRepo`, which archives the repo via `PATCH /repos/:o/:r {"archived":true}`. Without it, lock jobs fail 403 after exhausting retries and are marked `failed` with no operator alert. A branch-protection rule is a lower-privilege alternative for locking — see the Grading section. |
| **Contents** | Read & write | Cloning and pushing to student repos. |
| **Metadata** | Read | Required by GitHub for any App installation. |
| **Webhooks** | Read & write | `EnsureWebhook` registers a push webhook per repo so grading triggers on student pushes. |

## Forgejo / Gitea

> **Full walkthrough**: [`docs/forgejo-setup.md`](docs/forgejo-setup.md)

Quad is host-agnostic: the same interface (`pkg/adapter.Adapter`) that backs the
GitHub path also backs Forgejo and Gitea — both implement the same `/api/v1` REST
API, so one adapter targets both. Provisioning, locking, webhooks, and grading
orchestration work identically; only the auth mechanism differs (a static token
instead of a GitHub App JWT).

```sh
export QUAD_FORGEJO_BASE_URL=https://forgejo.example.org  # instance root
export QUAD_FORGEJO_TOKEN=...                              # personal/admin token
go run ./cmd/quad
```

Both `QUAD_FORGEJO_BASE_URL` and `QUAD_FORGEJO_TOKEN` must be set together, or
neither. When unset, the Forgejo adapter is simply not registered and Forgejo
classrooms will fail to provision — set them to enable.

### Student self-enrollment and operator login

To allow students to self-enroll on a Forgejo-backed classroom (and optionally to
use Forgejo accounts for operator login), register an OAuth2 application on your
Forgejo instance and set:

```sh
export QUAD_FORGEJO_OAUTH_CLIENT_ID=...      # OAuth2 app client id
export QUAD_FORGEJO_OAUTH_CLIENT_SECRET=...  # OAuth2 app client secret
# QUAD_FORGEJO_BASE_URL and QUAD_OAUTH_REDIRECT_URL are reused from above
```

The redirect URI registered with the OAuth2 app must be the same
`QUAD_OAUTH_REDIRECT_URL` used for GitHub (e.g. `https://your-host/auth/callback`);
a single `/auth/callback` endpoint serves all hosts — the state parameter carries
the host so callbacks are routed correctly.

When multiple resolvers are configured, operator login defaults to GitHub. To
choose another (or to be explicit), set `QUAD_OPERATOR_HOST` to `github`,
`forgejo`, `gitea`, or `gitlab`:

```sh
export QUAD_OPERATOR_HOST=forgejo   # github | forgejo | gitea | gitlab
```

Because Forgejo and Gitea are one adapter family sharing the same API, configuring
`QUAD_FORGEJO_*` registers the instance under **both** the `forgejo` and `gitea`
host labels; a classroom declares whichever matches your actual server.

**Privacy**: self-enrollment stores only the student's Forgejo username and numeric
user id — no real name, email, or SIS id is requested or stored. The Forgejo API
scope is `read:user`; only the `login` and `id` fields are used from the response.

### Token permissions

The token (Settings → Applications → Access Tokens on the Forgejo UI) needs:

| Scope | Why |
|---|---|
| **Organization** — create | `EnsureNamespace` creates the org via the API if it doesn't exist. |
| **Repository** — read & write | Repo creation (generate from template), collaborator management, archiving (lock/unlock), webhook management. |
| **Issue** (not required) | — |

### Limitations

- **Template repos**: the template repository must be explicitly marked as a
  template on the Forgejo/Gitea instance (Settings → Repository → ✓ Template
  Repository). The generate endpoint returns an error if the source is not a
  template.
- **`IncludeAllBranches`**: Gitea's generate API copies only the default branch
  (`git_content: true`). `CreateRepoOptions.IncludeAllBranches` is accepted but
  cannot be honoured; all branches from the template will **not** be copied.

### Grading

The Forgejo adapter returns `ErrNotImplemented` for `DispatchGrading`, which
signals Quad's orchestrator to run grading on its own sandboxed container runners
— the same as GitHub. The grading checkout is host-aware: the clone host is derived
from the classroom's `host` field, so Forgejo repos are cloned from the correct
instance and can be graded end-to-end.

Private clones authenticate via `GIT_ASKPASS` — the token from `QUAD_FORGEJO_TOKEN`
is delivered to git through the environment only, never embedded in the clone URL or
process arguments (H1 credential hygiene). The URL carries only the non-secret
username (`oauth2` by default; override with `QUAD_FORGEJO_GIT_USERNAME` if your
instance requires the token owner's username instead).

> **Caveat**: Gitea/Forgejo HTTPS token auth via basic-auth username `oauth2` is the
> widely-supported convention but is not guaranteed across all instances. If a real
> Forgejo clone fails authentication during grading, set
> `QUAD_FORGEJO_GIT_USERNAME=<token-owner-username>` to match the instance's
> expectation.

## Grading

Grading runs untrusted student code, so the runner is chosen explicitly and
nothing executes student code unless configured:

```sh
export QUAD_GRADER=container            # sandboxed container runner (recommended)
export QUAD_GRADER_IMAGE=python:3.12    # default image (a spec may set its own)
export QUAD_GRADER_RUNTIME=docker       # or podman
export QUAD_GIT_CLONE_TOKEN=...         # GitHub token for private repo clones
QUAD_FORGEJO_TOKEN=...  go run ./cmd/quad   # Forgejo token reused for grading
```

The checkout is **host-aware**: clones are directed to the right instance
(`github.com`, a GHES host, or the Forgejo/Gitea/GitLab base URL) and authenticate
via `GIT_ASKPASS` — the token is passed to git through the process environment
only, never embedded in the clone URL or process arguments. The clone **scheme
follows each host's base URL** (`QUAD_FORGEJO_BASE_URL` / `QUAD_GITLAB_BASE_URL` /
`QUAD_GITHUB_BASE_URL`) and defaults to `https`, so a plain-HTTP instance (e.g.
`http://localhost:3000`) is cloned over `http` rather than forced through TLS. Two
optional env vars affect clone behaviour:

| Variable | Default | Purpose |
|---|---|---|
| `QUAD_GITHUB_BASE_URL` | _(github.com)_ | GHES: set to your enterprise URL so the clone target matches your API base. |
| `QUAD_FORGEJO_GIT_USERNAME` | `oauth2` | URL username for Forgejo HTTPS auth. Change to the token owner's username if your instance requires it. |

The container runner enforces `gradingspec.Limits` on every step, with fail-safe
defaults even when the spec omits them: `--network none` (egress denied),
`--memory`/`--cpus`/`--pids-limit`, `--cap-drop ALL`, `--security-opt
no-new-privileges`, a read-only rootfs with only the checkout and `/tmp`
writable. The host clones the repo (cloning is not code execution) and mounts it;
only the spec's commands run inside the container, so a local runtime daemon is
required. `network: restricted` attaches the operator-provided
`QUAD_GRADER_RESTRICTED_NETWORK`; with none configured it falls back to `none`, so
"restricted" never silently means "open".

Containers run as the **server process's own uid:gid** by default (so the
bind-mounted checkout directory is writable without relaxing its permissions). Set
`QUAD_GRADER_USER` to override — for example to a fixed non-root uid. If the server
runs as root, containers also run as root, but remain constrained by `--cap-drop ALL`,
`--security-opt no-new-privileges`, `--read-only`, and `--network none`. For
production, run the server as a dedicated non-root user.

`QUAD_GRADER=local-exec-unsafe` selects the host exec runner, which has **no
isolation** (only a timeout) and is for trusted/local material only.

## Webhooks (auto-regrade on push)

When `QUAD_WEBHOOK_BASE_URL` is set, Quad registers a push webhook on each
provisioned repo at `<base>/webhooks/<host>` — so a delivery always reaches the
handler that knows how to verify that host. A student `git push` then hits the
receiver, which authenticates the delivery (HMAC signature for GitHub/Gitea/Forgejo;
the `X-Gitlab-Token` header for GitLab) and enqueues a regrade keyed to the head
commit (so each push grades once; a new commit regrades).

```sh
# Public BASE URL of this Quad instance. Quad appends /webhooks/<host> per repo.
# It must be reachable BY THE GIT HOST, not just by you.
export QUAD_WEBHOOK_BASE_URL=https://your-host   # (QUAD_WEBHOOK_URL is a deprecated alias)

# Per-host signing secret; set the same value in the host's webhook config.
export QUAD_FORGEJO_WEBHOOK_SECRET=$(openssl rand -hex 32)   # covers forgejo AND gitea
export QUAD_GITHUB_WEBHOOK_SECRET=$(openssl rand -hex 32)
export QUAD_GITLAB_WEBHOOK_SECRET=$(openssl rand -hex 32)
```

A host with no configured secret has its deliveries rejected (the receiver returns
`404`); a delivery that fails authentication returns `401`. The startup summary
prints the webhook base URL and the set/unset state of each host's secret.

> **Reachability gotchas.** `localhost` from inside a Forgejo/GitLab container is the
> container, not your machine — use the host address (e.g.
> `http://host.docker.internal:8080` on Docker Desktop). Cloud GitHub and gitlab.com
> cannot reach `localhost` at all; use a tunnel for local testing. See the
> [Forgejo](docs/forgejo-setup.md), [GitHub](docs/github-setup.md), and
> [GitLab](docs/gitlab-setup.md) guides.

## Student experience

After accepting an assignment, a student lands on **`/me`** — a lightweight,
framework-free page (served whether or not the React dashboard is mounted). There
they see each of their submissions: assignment and classroom, a link to their repo,
the deadline, submission status, live grading status, the latest score as
`score / max_score`, and an expandable per-test breakdown plus attempt history.
Returning students sign back in at **`/student/login`**.

The student API is strictly own-data: `GET /me/work` and `GET /me/work/{id}` are
scoped to the caller's Git username and host, and a request for a submission the
caller doesn't own returns `404` — never revealing another student's work. As
everywhere in Quad, no student names, SIS IDs, or plaintext emails are stored or
shown; the identity anchor is the Git username.

## Licensing

A deliberate split (see [`DESIGN.md`](DESIGN.md) §11):

- **Control plane** (`cmd/`, `internal/`) — **AGPL-3.0-or-later**. Network
  copyleft keeps the platform community-owned.
- **Interoperability primitives** (`pkg/adapter`, `pkg/gradingspec`) —
  **Apache-2.0**. The pieces that make Quad interoperable are maximally reusable.

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md) and our [code of conduct](CODE_OF_CONDUCT.md).
Good first targets: implementing the GitHub adapter methods (`pkg/adapter/github`)
and the MVP vertical-slice endpoints (`internal/api`).
