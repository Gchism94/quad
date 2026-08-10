# CC-CA18 — document the GitHub App private-key mount; fix the restart-vs-up trap

## Context

Wiring the GitHub host adapter into the pilot droplet (2026-08-10) hit two
real gaps in the deploy tooling, neither hypothetical — both cost real
round-trips getting the droplet working:

1. **No documented path for the private key to reach the container.**
   `CAIRN_GITHUB_PRIVATE_KEY_FILE` is a file path the `cairn` process opens
   at runtime, but `deploy/docker-compose.yml`'s `cairn` service has no
   volume that exposes any host path into the container, and the container
   runs as a non-root user (`USER cairn` in the Dockerfile). Pointing
   `CAIRN_GITHUB_PRIVATE_KEY_FILE` at a host path (e.g. `/root/cairn/github-app.pem`)
   fails two different ways depending on what's actually true inside the
   container: `permission denied` if the path happens to resolve into a
   directory the `cairn` user can't traverse (e.g. anything under `/root`),
   or `no such file or directory` once you realize it needs a bind mount and
   add one to the wrong service block by mistake (this happened live — the
   mount line landed under `postgres:`'s `volumes:` instead of `cairn:`'s;
   both blocks are named `volumes:` and are easy to conflate when editing by
   hand). Neither `docs/github-setup.md` nor `docs/deploy.md` currently says
   anything about a container-side path or a required mount at all.

2. **`docker compose restart` does not reread `.env` or the compose file.**
   After editing `.env` with the new GitHub vars, `docker compose restart
   cairn` reported success but the container kept running with its
   original environment — `restart` only restarts the existing container
   process, it doesn't recreate it. `docker compose up -d` (recreating the
   container) is what's actually needed after any `.env` or compose-file
   change. This isn't called out anywhere in `docs/deploy.md`, and it's a
   generically surprising Compose behavior worth a one-line callout so the
   next person doesn't lose a round-trip to it either.

## What to change

### 1. `deploy/docker-compose.yml`

Add a bind mount for the GitHub App private key under the **`cairn:`
service's** `volumes:` list (verify it lands there, not `postgres:`'s):

```yaml
      - ./github-app.pem:/run/secrets/github-app.pem:ro
```

Use a path relative to this file's own directory (`deploy/`), matching the
existing `env_file: - ../.env` convention already in this file — so the
operator-facing convention is: place the downloaded `.pem` at the
**repository root** as `github-app.pem` (sibling to `.env`), and Compose
picks it up automatically. Confirm the relative-path resolution actually
matches `../.env`'s (i.e. relative to `deploy/`, not the top-level
`compose.yaml`'s directory) before finalizing — don't assume, verify by
running `docker compose config` and checking the resolved `source:` path.

**Known tradeoff to document, not solve:** if an operator uses Forgejo or
GitLab only (no GitHub), this bind-mount source won't exist on disk. Docker
auto-creates an empty directory at a missing bind-mount source rather than
failing. Add a comment directly above the mount line in the compose file
warning that a stray empty `github-app.pem/` directory at the repo root is
expected/harmless if you don't use the GitHub adapter — don't try to make
the mount conditional (Compose profiles for a single mount line is more
complexity than this warrants); just document the artifact so it isn't
mistaken for a bug later.

### 2. `docs/github-setup.md`

In the private-key step (§2, step 3), make explicit:
- The key must be placed at the repository root as `github-app.pem` (next
  to `.env`) for the Docker Compose deploy path specifically — distinct
  from the bare-metal/non-container instructions the doc may also cover.
- `CAIRN_GITHUB_PRIVATE_KEY_FILE` should be set to the **container-side**
  path, `/run/secrets/github-app.pem` — not the host path — when running
  via `deploy/docker-compose.yml`. State plainly that these are two
  different paths (host vs. container) and which one the env var wants.

### 3. `docs/deploy.md`

Add a short, visible callout (near wherever `.env` edits are discussed, or
as its own small subsection) stating: after changing `.env` or any compose
file, use `docker compose up -d [service]` to apply it — `docker compose
restart` reuses the existing container and will not pick up the change.
Mention `--force-recreate` as the fallback if a plain `up -d` doesn't
appear to take effect.

## Verification (do not skip)

- `docker compose config` (from repo root) and confirm the private-key bind
  mount resolves under the `cairn` service specifically, with the source
  path you expect.
- Actually bring up the stack locally (or reuse an existing test
  environment) with a dummy `github-app.pem` file at the repo root and
  confirm `cairn doctor` can at least attempt to open it (a parse/format
  failure from a dummy key is fine and expected — the point is confirming
  the file is reachable inside the container at the documented path, not
  validating a real key).
- Confirm `docker compose config` / `up -d` still work cleanly for an
  operator who has **not** created `github-app.pem` (Forgejo/GitLab-only
  case) — verify what actually happens (stray directory, as expected) and
  that nothing else breaks.
- `go build ./... && go vet ./... && gofmt -l .` — no code changed here,
  but run it anyway since this is a mechanical-but-real deploy config change
  and regressions have shown up from smaller edits before.
- Re-read the diff before committing. This prompt is docs/config only — no
  source files should appear in `git status` after you're done.

## Explicitly out of scope

- Do **not** touch the live pilot droplet's `.env` or
  `deploy/docker-compose.yml` — those are already hand-patched and working
  (verified via `cairn doctor --verify-hosts`, 2026-08-10). This prompt
  fixes the **repository** so the next clone/deploy gets it right from the
  start; reconciling the droplet's live files with this commit (via `git
  pull` + reapplying local secrets) is a separate, later step, not part of
  this one.
- Do not attempt to make the private-key mount conditional via Compose
  profiles — documented tradeoff (stray empty directory when unused) is
  the intentionally simpler path per above.
- HTTPS/webhook-URL-scheme (the `webhooks ... not https://` warning) stays
  out of scope, same as CC-CA16 — still deferred pending a real domain.
