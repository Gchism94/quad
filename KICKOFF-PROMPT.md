# Kickoff prompt for Claude Code — Cairn pilot sprint

Repo: `~/dev/cairn` (Cairn; remote `EduCloud-Ecosystem/cairn`). Goal: **pilot-ready
before the August 28 GitHub Classroom shutdown**, running semester-long in the
maintainer's own course from the first assignment. Three sessions, in order.
Open Claude Code in the repo and paste the session prompt. Start in plan mode.

---

## Operating constraints for every session on this repo

```
1. KEEP THE ADAPTER SEAM CLEAN. No GitHub-specific logic outside
   pkg/adapter/github. The host-agnostic interface is the product; the GitHub
   path is one plugin among three.

2. SANDBOX LIMITS ARE NOT NEGOTIABLE. Nothing may weaken the container
   runner's gradingspec.Limits enforcement (network/memory/cpu/pids, dropped
   caps, read-only rootfs). The host-exec runner stays explicitly marked
   unsafe/local.

3. TESTS STAY GREEN. `go test ./...` passes after every session; web/ builds
   (`npm run build`). New behavior ships with tests that run without network.

4. TREAT GITHUB CLASSROOM'S EXPORT SURFACE AS UNVERIFIED. Before writing the
   importer, inspect a real classroom (the maintainer's own): what the roster
   CSV actually contains, how assignment repos are named in the org, what
   survives the shutdown (working hypothesis: classroom metadata is deleted
   Sept 4, but repos in the org remain as ordinary repos — verify). Record
   findings in docs/ghc-import.md before coding.

   Desk research done 2026-08-05 (Cowork), to start the investigation from,
   not to replace it — none of this was checked against the maintainer's
   actual classroom:
   - The working hypothesis is confirmed by GitHub's own retirement notice:
     classroom-specific data (classroom/assignment names, tests defined
     outside repos, historical test-run data, submissions, LTI-integrated
     rosters) is permanently deleted, final deletion September 4, 2026;
     accounts, repositories, and organizations are explicitly stated as
     unaffected and persist indefinitely.
   - GitHub publishes an official export tool,
     `github-education-resources/classroom-export-utility`
     (`export-classrooms.sh`, `gh auth login` + the `gh classroom` CLI
     extension + `jq`), producing a directory tree per classroom:
     `classrooms.json`, per-classroom `assignments.json`, and per-assignment
     `assignment.json` + `accepted-assignments.json` + `grades.csv`. The
     accepted-assignments/grades files already carry GitHub username, roster
     identifier, **repository URL**, submission timestamp, and grade
     together — if accurate, this removes the need to infer the
     assignment-prefix repo-naming convention by pattern-matching the org,
     since the repo URL is already in the data.
   - Caveat, not to be skipped: this tool shows minimal maintenance history
     (a handful of commits, a couple of open issues) and is not the same
     thing as a first-party, load-bearing GitHub API. Verify its output
     against the real org before trusting it as the importer's primary input
     format — confirm the JSON schema actually matches what's described here,
     and confirm it runs cleanly end to end, before building `cairn import
     ghc` against it. If it's unreliable, fall back to the original plan
     (roster CSV + repo-naming convention inference).
   - Sources: [GitHub Classroom retirement FAQ](https://github.com/orgs/community/discussions/145312),
     [Classroom sign-ups no longer available](https://github.blog/changelog/2026-05-26-github-classroom-sign-ups-are-no-longer-available/),
     [Export or migrate GitHub Classroom data](https://docs.github.com/en/education/manage-coursework-with-github-classroom/get-started-with-github-classroom/export-or-migrate-github-classroom-data),
     [classroom-export-utility](https://github.com/github-education-resources/classroom-export-utility).

5. UPDATE ROADMAP.md CHECKBOXES as part of every session's definition of
   done. The file drifted from the commits once already.

Ask me before: changing the store interface or schema, adding a dependency,
anything touching sandbox limits, or any non-dry-run operation against the
live classroom org.
```

## Session 1 — GitHub Classroom import

```
Scope: the migration path. This is the highest-value unbuilt feature in the
platform: it is the difference between an alternative and a replacement.

1. Inspect first (constraint 4): document the real export surface in
   docs/ghc-import.md.
2. Build `cairn import ghc --org <org> --roster <roster.csv> [--dry-run]`:
   - create a Cairn classroom from the Classroom org
   - bind usernames from the roster CSV (username + identifier columns as
     found in step 1)
   - register assignments by recognizing the repo-naming convention
     (assignment-prefix per student/team), pointing at the existing repos
     rather than mass-cloning, since the org outlives the shutdown
   - idempotent: re-running is safe; --dry-run prints the full plan and
     touches nothing
3. Unit-test the importer against fixture CSVs and repo lists — no network.

Accept: dry-run against the maintainer's real classroom org prints a correct
plan; the live run creates classroom + assignments + bindings; go test green.
Deliverable: docs/ghc-import.md findings note + the importer.
```

## Session 2 — doctor + one-command deploy

```
Scope: the paved path an instructor can follow alone.

1. `cairn doctor`: check DB reachability (and which store build is running),
   host-adapter credentials, webhook secret configuration, container runtime
   presence, port availability. Every failure prints what to do about it.
2. Verify deploy/docker-compose.yml on a clean VM end to end; fix what breaks;
   write docs/deploy.md (the exact commands, nothing implied).

Accept: fresh VM → docker compose up → `cairn doctor` green → instructor
creates a classroom in the dashboard. ROADMAP boxes updated.
```

## Session 3 — student views + migration guide

```
Scope: what students see, and the document that converts instructors.

1. Complete the student-facing pages on the existing student-session API
   (assignment list, repo link, deadline state, grade status).
2. Write docs/migrate-from-github-classroom.md — the afternoon path:
   import → verify roster → first assignment → students join. Test it by
   following it verbatim on the pilot course.

Accept: student flow works end to end with a dummy account; the guide has
been executed as written, not just read. ROADMAP boxes updated.
```

## After the sprint

Deploy on the Waypoint host when it exists (PLAN.1 Phases 2–3), migrate the
pilot course, and run the semester. Grading-at-scale tests on Jetstream2
follow under the ACCESS allocation before any multi-course rollout.
