# GitLab setup walkthrough

This guide wires Quad to GitLab — gitlab.com (free, easiest) or a self-hosted
instance (`QUAD_GITLAB_BASE_URL`). GitLab is a third first-class host alongside
GitHub and the Gitea family; the dashboard, grading spec, and workflow are
identical regardless of host.

> The GitLab adapter is implemented against the `/api/v4` REST API — this is
> configuration only, no code.

---

## GitLab specifics worth knowing

- **Namespaces are groups.** A classroom's `host_namespace` is a GitLab group path.
  Quad creates the group via the API if it doesn't exist.
- **Assignments are forks.** GitLab has no "create from template" endpoint, so Quad
  forks the template project into the group and then breaks the fork relationship
  so the student project is independent. The fork may finish importing
  asynchronously — the project appears immediately; its content lands shortly after.
- **HTTPS clone uses `oauth2:<token>`** — `oauth2` is the username, the token is the
  password (the opposite of Forgejo). This is the `QUAD_GITLAB_GIT_USERNAME`
  default.
- **Webhooks are not HMAC-signed.** GitLab sends the configured secret verbatim in
  the `X-Gitlab-Token` header; Quad compares it in constant time.

---

## Prerequisites

- A GitLab account on gitlab.com or a self-hosted instance you administer.
- Go 1.25+ on the machine running Quad.

---

## 1. Create a group and a template project

1. Create a **group** that will own classroom repositories, e.g. `cs101`. This is
   the classroom's `host_namespace`.
2. Create a **template project** (e.g. `instr/hw1-template`) and push your starter
   code. The operator's token (below) must be able to fork it — keep it visible to
   the token owner (internal/public, or owned by them).

---

## 2. Personal access token

GitLab: **User Settings → Access Tokens → Add new token**, with the **`api`**
scope (group creation, project fork, member and webhook management).

```sh
export QUAD_GITLAB_BASE_URL=https://gitlab.com     # or your self-hosted URL
export QUAD_GITLAB_TOKEN=glpat-xxxxxxxxxxxxxxxxxxxx
export QUAD_GITLAB_GIT_USERNAME=oauth2             # default; GitLab HTTPS clone convention
```

---

## 3. OAuth application (student self-claim + operator login)

GitLab: **User Settings → Applications → Add new application**.

| Field | Value |
|---|---|
| Name | `Quad` |
| Redirect URI | `http://<quad-host>:8080/auth/callback` |
| Confidential | ✓ (checked) |
| Scopes | `read_user` |

Copy the generated **Application ID** and **Secret**.

```sh
export QUAD_GITLAB_OAUTH_CLIENT_ID=<application-id>
export QUAD_GITLAB_OAUTH_CLIENT_SECRET=<secret>
export QUAD_OAUTH_REDIRECT_URL=http://<quad-host>:8080/auth/callback
```

The redirect URI must match `QUAD_OAUTH_REDIRECT_URL`. A single `/auth/callback`
serves every host; the state parameter carries the host so callbacks route
correctly.

**Privacy**: the `read_user` scope yields only the GitLab `username` and numeric
`id`. No real name, email, or SIS id is requested or stored; the numeric id is the
durable identity anchor (so a renamed student keeps the same row).

---

## 4. Start Quad

A complete operator environment (concrete values; operator `alice`, group `cs101`),
all in the **same shell** that runs Quad:

```sh
export QUAD_GITLAB_BASE_URL=https://gitlab.com
export QUAD_GITLAB_TOKEN=glpat-xxxxxxxxxxxxxxxxxxxx
export QUAD_GITLAB_GIT_USERNAME=oauth2
export QUAD_GITLAB_OAUTH_CLIENT_ID=abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789
export QUAD_GITLAB_OAUTH_CLIENT_SECRET=gloas-secret-value-here
export QUAD_OAUTH_REDIRECT_URL=http://localhost:8080/auth/callback
export QUAD_OPERATOR_HOST=gitlab
export QUAD_ADMIN_USERS=alice
# grading (optional, step 8):
export QUAD_GRADER=container
export QUAD_GRADER_IMAGE=python:3.12

go run ./cmd/quad
```

Startup summary (abridged):

```
store: sqlite /absolute/path/to/quad.db
adapters registered: gitlab
identity resolvers: gitlab  operator-host: gitlab
quad control plane listening on :8080
```

---

## 5. Tiered live validation

1. **Operator login** — visit `http://localhost:8080/auth/login`, authenticate as
   `alice`, then:
   ```sh
   curl -s --cookie "quad_session=…" http://localhost:8080/auth/me   # {"username":"alice", ...}
   ```
2. **Classroom → assignment** — `host: gitlab`, the group as `host_namespace`:
   ```sh
   curl -s -X POST http://localhost:8080/classrooms \
     -H "Content-Type: application/json" \
     -d '{"name":"CS 101","host":"gitlab","host_namespace":"cs101"}'

   curl -s -X POST http://localhost:8080/classrooms/<classroom-id>/assignments \
     -H "Content-Type: application/json" \
     -d '{"title":"Homework 1","slug":"hw-1",
          "template":{"host":"gitlab","namespace":"instr","name":"hw1-template"}}'
   ```
3. **Student claim** — open `http://localhost:8080/assignments/<assignment-id>/accept`
   in a private window, authenticate as a student. Quad provisions
   `cs101/hw-1-<student>` (a fork with the relationship broken), adds the student as
   a project member, and lands them on **`/me`** with their repo link, deadline, and
   grading status. If the roster user is already an Owner/Maintainer of the target
   group (e.g. the instructor testing with their own account, or a TA), Quad skips
   the redundant member add — they already have access.
4. **Grading + push regrade** — trigger grading; clones use `oauth2:<token>`. With a
   webhook configured (step 7), a `git push` re-runs grading and `/me` updates live.

---

## 6. Configure grading (optional)

```sh
export QUAD_GRADER=container
export QUAD_GRADER_IMAGE=python:3.12   # default; specs may override
export QUAD_GRADER_RUNTIME=docker      # or podman
```

The grading checkout clones GitLab repos as `oauth2:<QUAD_GITLAB_TOKEN>` over HTTPS;
the token is delivered via `GIT_ASKPASS`, never embedded in the URL. Trigger a run:

```sh
curl -s -X POST \
  http://localhost:8080/classrooms/<classroom-id>/assignments/<assignment-id>/grade
# {"status":"grading","jobs_enqueued":N,"skipped_unprovisioned":M}
```

---

## 7. Webhooks (auto-regrade on push)

Quad registers the webhook on each project when `QUAD_WEBHOOK_BASE_URL` is set,
appending `/webhooks/gitlab` per project. GitLab authenticates the delivery with the
`X-Gitlab-Token` header (not an HMAC).

```sh
# Public BASE URL of Quad. Quad appends /webhooks/gitlab per repo. Must be
# reachable BY GITLAB.
export QUAD_WEBHOOK_BASE_URL=https://your-quad-host
export QUAD_GITLAB_WEBHOOK_SECRET=$(openssl rand -hex 32)
```

Restart Quad and confirm the startup summary shows the webhook base URL and
`webhook secret [gitlab]: set`.

> **Reachability gotcha.** The webhook is called by **GitLab**, not your laptop. On
> gitlab.com it comes from the internet and **cannot reach `localhost`** or a
> private LAN address — use a tunnel for local testing. For a self-hosted GitLab in
> a container, `localhost` is the container; use the host address (e.g.
> `http://host.docker.internal:8080` on Docker Desktop). Note that gitlab.com blocks
> webhooks to local/private addresses by default (an instance setting on
> self-hosted).

---

## 8. Troubleshooting

| Symptom | Cause / fix |
|---|---|
| Group not found / create fails | The token lacks the `api` scope or permission to create top-level groups. Create the group manually and grant the token access, or use an existing group as `host_namespace`. |
| Fork fails | The template project isn't accessible to the token (visibility/ownership), or the token can't fork into the target group. Make the template internal/public or owned by the token user. |
| Member add fails (`no user with username`) | The student has no account on this GitLab instance yet, or never logged in. They must have an account; usernames resolve via `GET /users?username=`. |
| Webhook delivery `401` | The `X-Gitlab-Token` doesn't match `QUAD_GITLAB_WEBHOOK_SECRET`. Re-set both to the same value. |
| Webhook `204`, no grading | The push didn't match a tracked project (wrong group/path), or it was a non-push event (`object_kind` ≠ `push`, e.g. a tag push). |
| Clone `401` during grading | `oauth2:<token>` failed — confirm `QUAD_GITLAB_TOKEN` has `read_repository`/`api` and access to the project. As a fallback, set `QUAD_GITLAB_GIT_USERNAME` to the token owner's username. |
| Fork/import still empty | The fork import is still running (asynchronous). Quad tolerates this — content arrives shortly; re-grade once it lands. |

---

## Next

A single Quad deployment can serve GitLab, GitHub, and Forgejo/Gitea classrooms at
once — each classroom carries its own host. See the
[GitHub](github-setup.md) and [Forgejo](forgejo-setup.md) guides, and the
[migration notes](migrating-github-to-forgejo.md) for moving courses between hosts.
