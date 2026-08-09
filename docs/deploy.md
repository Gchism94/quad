# Deploying Cairn

The whole control plane — server, dashboard, database — comes up with
`docker compose up -d`, and `cairn doctor` tells you what is still wrong and what
to type to fix it.

This document is the exact path. Nothing is implied: every command is here, in
order, and each one has been run on a fresh Ubuntu 24.04 machine.

**Time**: about fifteen minutes, most of it waiting for Docker to install.

---

## What you need

- A Linux VM you control. A 2 GB / 1 vCPU instance is enough for a single course;
  give grading 4 GB if you expect large classes. Ubuntu 24.04 is assumed below.
- A Git host to back your classrooms — GitHub, GitLab, or a self-hosted
  Forgejo/Gitea. Set it up with
  [`github-setup.md`](github-setup.md), [`gitlab-setup.md`](gitlab-setup.md), or
  [`forgejo-setup.md`](forgejo-setup.md). You can bring Cairn up first and add the
  host afterwards; `cairn doctor` will keep reminding you.
- Optionally a domain name pointed at the VM. You need one for OAuth login and
  push webhooks, but not to get started.

---

## 1. Install Docker

```sh
sudo apt-get update
sudo apt-get install -y ca-certificates curl git
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
  | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

Let your user run Docker without `sudo`, then re-open the session so the new
group applies:

```sh
sudo usermod -aG docker "$USER"
newgrp docker
docker run --rm hello-world
```

## 2. Get Cairn

```sh
git clone https://github.com/EduCloud-Ecosystem/cairn.git
cd cairn
```

## 3. Create the grading work directory

Grading clones each submission into this directory and bind-mounts it into a
sandboxed container. It is mounted at the **same path** inside the Cairn
container, because the Docker daemon resolves mount paths on the host — a
directory that existed only inside the container would be mounted empty and every
grading run would fail. `cairn doctor` verifies this round trip.

```sh
sudo mkdir -p /var/lib/cairn/work
sudo chown 1000:1000 /var/lib/cairn/work
```

`1000` is the uid Cairn runs as inside its container.

## 4. Write your `.env`

```sh
cp .env.example .env
```

Then set exactly these two:

```sh
# a password for the bundled PostgreSQL — any value, it never leaves this host
echo "POSTGRES_PASSWORD=$(openssl rand -hex 24)" >> .env

# lets the container use the Docker socket for grading without running as root
echo "DOCKER_GID=$(getent group docker | cut -d: -f3)" >> .env
```

Everything else in `.env.example` is optional and commented; add host credentials
when you have them.

> **Not `CAIRN_ADMIN_USERS` yet.** Operator login needs both an allowlist *and* a
> working OAuth app, and setting the allowlist without OAuth locks out everyone,
> including you — `cairn doctor` will (correctly) fail. Until §9 the server runs
> in **open mode**: anyone who can reach it can operate it. That is fine while it
> is a fresh VM nobody knows about, and §9 closes it before anyone else does.

> **Leave `CAIRN_DATABASE_URL` alone.** The compose stack builds it from
> `POSTGRES_PASSWORD` and points it at its own PostgreSQL. Same for
> `CAIRN_WEB_DIR` and `TMPDIR` — the deployment owns those three.

## 5. Start it

```sh
docker compose up -d
```

The first run builds the image (a few minutes). Afterwards:

```sh
docker compose ps
```

Both services should be `Up`, with `cairn` reaching `(healthy)` within about half
a minute.

## 6. Check it

```sh
docker compose exec cairn cairn doctor
```

Every failure prints the exact thing to change on the line below it. Exit status
is 0 when there are no failures, so this works in a script:

```sh
docker compose exec -T cairn cairn doctor || echo "not ready yet"
```

A brand-new deployment looks exactly like this. Three warnings and **no failures**
is the healthy starting point — the warnings are the things §8–§10 turn on:

```
cairn doctor

  ok    store           postgres — reachable
  ok    environment     no unrecognized CAIRN_* variables
  ok    listen address  :8080 is already served by a healthy Cairn
  warn  host adapter    no Git host is configured — classrooms can be created, but no repository can be provisioned
  warn  operator auth   open mode — CAIRN_ADMIN_USERS is unset, so the API and dashboard are unprotected
  ok    dashboard       serving from /srv/cairn/web
  warn  grading         disabled — grade requests will be rejected

4 ok, 3 warning(s), no failures — Cairn is usable; the warnings above are worth reading.
```

## 7. Open the dashboard

```
http://<your-server-ip>:8080
```

Create a classroom. That is the deployment working end to end.

Before students touch it, do §8 (HTTPS) and §9 (login) — until then the server is
plain HTTP and, with `CAIRN_ADMIN_USERS` unset, unauthenticated.

---

## 8. HTTPS

OAuth login and push webhooks both require a public HTTPS URL. The `tls` profile
runs [Caddy](https://caddyserver.com/) in front of Cairn and obtains a Let's
Encrypt certificate automatically.

Point a DNS `A` record at the VM first, then add to `.env`:

```sh
CAIRN_DOMAIN=cairn.example.edu
CAIRN_HTTP_BIND=127.0.0.1        # stop publishing plain HTTP on the public interface
CAIRN_WEBHOOK_BASE_URL=https://cairn.example.edu
CAIRN_OAUTH_REDIRECT_URL=https://cairn.example.edu/auth/callback
CAIRN_COOKIE_SECURE=1
```

```sh
docker compose --profile tls up -d
```

Certificate issuance takes a few seconds on first request. Watch it with
`docker compose logs -f caddy`.

## 9. Operator login — close the door

Until now the server has been in open mode. Locking it down takes two things
together, and they must be added in the same edit: an OAuth app so you can log
in, and an allowlist saying who may.

Create an OAuth app on your Git host (see its setup guide) with the callback set
to `https://<your-domain>/auth/callback`, then add all three at once:

```sh
CAIRN_GITHUB_CLIENT_ID=...
CAIRN_GITHUB_CLIENT_SECRET=...
CAIRN_ADMIN_USERS=your-github-username     # comma-separated for TAs
```

```sh
docker compose up -d
docker compose exec cairn cairn doctor
```

`cairn doctor` fails if `CAIRN_ADMIN_USERS` is set while no OAuth resolver is
configured, because that combination locks *everyone* out, including you. If you
see that failure, you added the allowlist without the OAuth credentials.

## 10. Autograding

Grading runs student code in a sandboxed container: no network by default,
dropped capabilities, read-only root filesystem, and memory/CPU/PID limits from
the grading spec. Enable it with:

```sh
CAIRN_GRADER=container
CAIRN_GRADER_IMAGE=python:3.12-slim   # or whatever your specs need
```

```sh
docker compose up -d
docker compose exec cairn cairn doctor
```

The `grading workdir` check runs a real container and confirms it can see the
directory Cairn writes checkouts into. If it fails, grading would silently
produce empty checkouts, so treat it as blocking.

> **Security note, stated plainly.** Cairn mounts `/var/run/docker.sock` so it can
> start grading containers. Access to that socket is equivalent to root on this
> host. That is why Cairn should run on a VM dedicated to it, and why only people
> you trust should be in `CAIRN_ADMIN_USERS`. Grading containers themselves are
> tightly confined; the socket is the part to think about.

### Isolation tiers — adding a kernel boundary

The hardening above is real, but a plain container **shares the host kernel**.
Every syscall student code makes is handled by the same kernel running your
server, and the container runtime is itself attack surface: `runc`
CVE-2024-21626 ("Leaky Vessels") reached the host root through a leaked
`/proc/self/fd` handle, and the November 2025 trio (CVE-2025-31133, -52565,
-52881) defeated masked paths and, in some configurations, AppArmor/SELinux.
Hardening flags raise the cost of an escape; they do not make one impossible.

`CAIRN_GRADER_ISOLATION` chooses what sits underneath those flags:

| Value | Boundary | When |
|---|---|---|
| `shared` (default, or unset) | Hardened container, **host kernel shared** | Trusted cohort; no gVisor available |
| `gvisor` | [gVisor](https://gvisor.dev) (`runsc`) userspace kernel — the sandbox sees ~53 host syscalls | Untrusted or unknown submissions |

Nothing changes for an existing deployment: leaving it unset is exactly the
behaviour Cairn has always had. The tier is opt-in.

**Installing gVisor**, then telling Cairn to use it:

```sh
# on the Docker host — see https://gvisor.dev/docs/user_guide/install/
sudo runsc install                 # registers runsc in /etc/docker/daemon.json
sudo systemctl restart docker
docker info --format '{{.Runtimes}}'   # runsc must appear here
```

```sh
CAIRN_GRADER_ISOLATION=gvisor
```

```sh
docker compose up -d
docker compose exec cairn cairn doctor    # the "grading isolation" line
```

**A requested tier is honoured or refused — never quietly downgraded.** If you
set `gvisor` and `runsc` is not registered with the daemon, Cairn does not fall
back to a shared-kernel container: `cairn doctor` fails with the fix, and
grading refuses to run. A deployment that believes it has a kernel boundary and
does not is worse off than one that never asked for it.

For the same reason, `CAIRN_GRADER_ISOLATION` is the *only* place the OCI
runtime is chosen. A `--runtime` entry in the runner's advanced `ExtraArgs` is
rejected at startup rather than merged, because `ExtraArgs` is appended last and
would otherwise override the tier you configured.

> **On podman:** `cairn doctor` cannot verify gVisor for you. Podman is
> daemonless — runtimes are declared in `containers.conf` and selected per
> container — so there is no registered-runtimes list to query, and doctor says
> so explicitly instead of guessing. Grading still works: it passes
> `--runtime runsc` per container, which is podman's own mechanism. Confirm it
> yourself with `podman run --rm --runtime runsc alpine true`.

**The honest performance cost.** CPU-bound work — the common case for
autograding — runs at roughly native speed. Syscall- and I/O-heavy work runs
**2–10× slower**, so a grading run that spawns many processes, writes many
files, or installs packages at test time will feel it. Measure your own specs
before mandating this across a course; the cost is real and worth knowing
rather than discovering at a deadline.

gVisor does not defend against bugs in gVisor's own Sentry, or against
microarchitectural side channels. It is a strong boundary, not a perfect one.
The tier vocabulary and the rationale behind it come from the platform's
isolation-tier spec (`outfitter/specs/08-isolation-tiers.md`), where this tier
is called `T-standard`; Cairn uses the plainer `shared`/`gvisor` names because
`T-standard` is the platform's recommended default, not Cairn's current one.

---

## Day-two operations

**Logs**

```sh
docker compose logs -f cairn
```

**Upgrade**

```sh
git pull
docker compose up -d --build
docker compose exec cairn cairn doctor
```

Schema migrations apply automatically on start.

**Back up** — everything durable is in PostgreSQL:

```sh
docker compose exec -T postgres pg_dump -U cairn cairn | gzip > cairn-$(date +%F).sql.gz
```

**Restore** into an empty stack:

```sh
gunzip -c cairn-2026-08-05.sql.gz | docker compose exec -T postgres psql -U cairn cairn
```

**Stop**, keeping data:

```sh
docker compose down
```

**Remove everything, including the database:**

```sh
docker compose down -v
```

---

## When something is wrong

Run `cairn doctor` first — it is written to answer this question, and each
failure carries its own fix. The table below covers what it cannot see.

| Symptom | Cause | Fix |
|---|---|---|
| `docker compose up` says a variable is not set | Running from the wrong directory | Run from the repository root; the root `compose.yaml` is what makes `.env` load |
| `env file /path/.env not found` | Step 4 skipped | `cp .env.example .env` |
| doctor: store unreachable | PostgreSQL still starting, or a hand-set `CAIRN_DATABASE_URL` | `docker compose ps`; remove any `CAIRN_DATABASE_URL` from `.env` |
| doctor: grading permission denied | `DOCKER_GID` wrong or unset | `echo "DOCKER_GID=$(getent group docker \| cut -d: -f3)" >> .env`, then `docker compose up -d` |
| doctor: grading workdir not visible | Work directory missing or not mounted at the same path | Redo §3; confirm `CAIRN_WORK_DIR` matches the directory you created |
| Dashboard loads, login redirects fail | `CAIRN_OAUTH_REDIRECT_URL` differs from the host's OAuth app | Make both exactly `https://<domain>/auth/callback` |
| Pushes do not trigger regrading | Webhook base URL not public, or no signing secret | Set `CAIRN_WEBHOOK_BASE_URL` to the HTTPS URL and `CAIRN_<HOST>_WEBHOOK_SECRET`; doctor checks both |
| Caddy logs `Cannot issue for "cairn-domain-not-set.invalid"` | `CAIRN_DOMAIN` is unset — that placeholder is what an unset domain becomes | Set `CAIRN_DOMAIN` in `.env`, then `docker compose --profile tls up -d` |
| Caddy retries `could not get certificate` for your real domain | DNS not pointing here yet, or 80/443 blocked | `dig +short <domain>` should return this VM's IP; Let's Encrypt must reach port 80 |

Still stuck: `docker compose logs cairn` usually says it outright — the startup
banner lists every wired component and what is missing.
