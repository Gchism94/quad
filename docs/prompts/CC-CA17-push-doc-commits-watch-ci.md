# CC-CA17 — Push the ROADMAP/CC-CA15/CC-CA16 commits, watch CI

*Claude Code prompt. Authored in Cowork, 2026-08-10. Small and mechanical —
`main` is 3 commits ahead of `origin/main` again, all doc/prompt files, no
code changes:*

```
71c360e docs: add CC-CA16 prompt - production deploy on the reused droplet, open mode
45dbd23 docs: add CC-CA15 prompt - purge-after-confirmed-export and roster-entry deletion
58d1d06 docs: update ROADMAP - CC-CA13/CC-CA14 closed the accent-folding gap
```

*This session's own sandbox (Cowork) cannot push — no network access to
GitHub from there. Run this from a session with real access.*

## 1. Confirm before pushing

- `git log origin/main..main --oneline` — confirm it shows exactly these
  three commits (or whatever's actually ahead by the time this runs; report
  the real list rather than assuming it matches what's written above).
- `git status` — confirm nothing else is staged/uncommitted that shouldn't
  go along for the ride. (Cowork's own working tree separately has three
  pre-existing modified-but-uncommitted files — `.claude/settings.json`,
  `docs/prompts/CC-CA12-verify-and-push.md`, `docs/prompts/CC-CA8-frontend-test-runner.md`
  — that are unrelated stray changes, not part of this push. Leave them
  alone; don't stage or commit them here.)

## 2. Push

`git push origin main`.

## 3. Watch CI

These are doc-only commits — no Go or frontend source changed — so CI
passing is close to guaranteed, but confirm it for real rather than
assuming: watch the workflow run triggered by this push
(`gh run watch` or the Actions tab) to completion.

## 4. Report

- The pushed commit range and confirmation `origin/main` now matches local
  `main`.
- The CI run's URL/ID and outcome.
- If anything was actually ahead besides the three commits listed above,
  say so plainly rather than silently pushing more than expected.
