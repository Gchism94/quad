# Migrating from GitHub to Forgejo (or Gitea)

Cairn's design goal is that the Git host is a **plugin, not a foundation**. You can
start on GitHub — zero new infrastructure, familiar to anyone coming from GitHub
Classroom — and later move courses onto a self-hosted Forgejo or Gitea instance
the institution controls, without rebuilding your materials. This is the honest
account of what transfers, what doesn't, and how to do it.

---

## What is fully portable

These carry over unchanged — they are host-neutral by construction:

- **Assignment template repositories.** They're ordinary Git repos. Push the same
  template to the new host and mark it as a template there.
- **The grading spec (`grading.json`) and the runner.** Grading runs on Cairn's own
  sandboxed container runner, not on host-native CI, so the same spec produces the
  same results regardless of host.
- **The instructor dashboard and the entire workflow.** Creating classrooms and
  assignments, rosters, deadlines, locking, grading, CSV export — identical on
  every host. The host is just a field on the classroom.

In short: **everything you author is host-independent.** Moving hosts is a
configuration change, not a rewrite.

---

## What does not transfer (and why it doesn't matter)

- **Student identity.** A GitHub username is not a Forgejo username — there is no
  mapping between accounts on different hosts. But rosters are rebuilt each course
  or term anyway (students self-claim with their account on the new host), so this
  is not a real loss. Cairn stores only the host username and numeric id, so there's
  nothing sensitive to migrate either.
- **Existing student repositories.** Repos already created on GitHub stay on
  GitHub. You do **not** lift a live, in-progress class to another host. You migrate
  by pointing *new* courses at the new host — the old course finishes where it is.

---

## One deployment can serve both hosts at once

A single Cairn instance can run GitHub **and** Forgejo/Gitea classrooms
simultaneously. Each classroom carries its own `host`, and Cairn keys its adapter
and resolver maps by host — so a `host: github` classroom and a `host: forgejo`
classroom coexist in the same server, each provisioning against the right place.

> **One caveat:** operator *login* uses a single configured `CAIRN_OPERATOR_HOST`
> (e.g. `github`), even though *assignments* may target any configured host. So you
> log in once with your operator account on the chosen host, and from there create
> classrooms on whichever host you like.

Because Forgejo and Gitea are one adapter family sharing the same API, configuring
`CAIRN_FORGEJO_*` registers the instance under **both** the `forgejo` and `gitea`
host labels — declare whichever matches your actual server.

---

## The realistic playbook

1. **Start on GitHub.** Configure the GitHub App and OAuth
   ([github-setup.md](github-setup.md)). Run a term with zero new infrastructure
   and no learning curve for students.
2. **Author once.** Build your template repos and `grading.json` specs. These are
   the durable assets — and they're host-neutral.
3. **Stand up Forgejo (or Gitea).** Follow [forgejo-setup.md](forgejo-setup.md) to
   configure `CAIRN_FORGEJO_*` on the same Cairn deployment. The startup summary will
   then list `github`, `forgejo`, and `gitea` as registered adapters.
4. **Point next term at the self-hosted instance.** Push your templates to the
   Forgejo org, then create the new classroom with `host: forgejo` (or
   `host: gitea`). Same tool, same templates, same specs — now on infrastructure
   the institution owns, with student records that never leave it.

You can run step 4 for one course while other courses stay on GitHub. There is no
flag day, no export/import, and no change to how you work.

---

## Checklist

- [ ] Template repos pushed to the new host and marked as templates
- [ ] `CAIRN_FORGEJO_*` configured on the deployment (adapter + OAuth)
- [ ] Startup summary lists the new host among registered adapters
- [ ] A test classroom created with `host: forgejo` / `host: gitea`
- [ ] A student self-claim succeeds against the new host in a private window
- [ ] A grading run returns the expected score
