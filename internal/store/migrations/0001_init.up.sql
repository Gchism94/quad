-- 0001_init.up.sql — Cairn initial schema.
--
-- PRIVACY: this schema has no column for a student's legal name, SIS ID, or
-- plaintext email. A student's identity anchor is their Git-host username
-- (roster_entries.host_username). See DESIGN.md sections 5 and 6.

CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    host          TEXT NOT NULL,
    host_user_id  TEXT NOT NULL,
    host_username TEXT NOT NULL,
    email         TEXT NOT NULL,                 -- operator (instructor/TA), not a student
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (host, host_user_id)
);

CREATE TABLE IF NOT EXISTS classrooms (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,                -- course label; metadata, not student PII
    host           TEXT NOT NULL,
    host_namespace TEXT NOT NULL,
    created_by     TEXT REFERENCES users(id),     -- the operator who created it, when known
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS assignments (
    id            TEXT PRIMARY KEY,
    classroom_id  TEXT NOT NULL REFERENCES classrooms(id) ON DELETE CASCADE,
    title         TEXT NOT NULL,
    slug          TEXT NOT NULL,
    template_host TEXT NOT NULL,
    template_ns   TEXT NOT NULL,
    template_name TEXT NOT NULL,
    template_ref  TEXT NOT NULL DEFAULT '',
    type          TEXT NOT NULL CHECK (type IN ('individual','group')),
    deadline      TIMESTAMPTZ,
    grading_spec  TEXT NOT NULL DEFAULT 'grading.json',
    access_policy TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (classroom_id, slug)
);

-- PRIVACY-CRITICAL TABLE.
-- No name, no SIS id, no plaintext email. email_hash is a salted one-way hash
-- used only for client-side re-matching; it is never reversible to an address.
CREATE TABLE IF NOT EXISTS roster_entries (
    id            TEXT PRIMARY KEY,
    classroom_id  TEXT NOT NULL REFERENCES classrooms(id) ON DELETE CASCADE,
    host          TEXT NOT NULL,
    host_username TEXT NOT NULL,
    email_hash    TEXT,
    status        TEXT NOT NULL DEFAULT 'invited'
                  CHECK (status IN ('invited','active','removed')),
    claimed_at    TIMESTAMPTZ,
    UNIQUE (classroom_id, host, host_username)
);

CREATE TABLE IF NOT EXISTS submissions (
    id               TEXT PRIMARY KEY,
    assignment_id    TEXT NOT NULL REFERENCES assignments(id) ON DELETE CASCADE,
    roster_entry_id  TEXT NOT NULL REFERENCES roster_entries(id) ON DELETE CASCADE,
    repo_host        TEXT NOT NULL,
    repo_namespace   TEXT NOT NULL,
    repo_name        TEXT NOT NULL,
    latest_commit    TEXT NOT NULL DEFAULT '',
    last_activity_at TIMESTAMPTZ,
    status           TEXT NOT NULL DEFAULT '',
    UNIQUE (assignment_id, roster_entry_id)
);

-- An education record when joined to submission + roster_entry. See DESIGN.md 10.
CREATE TABLE IF NOT EXISTS grades (
    id            TEXT PRIMARY KEY,
    submission_id TEXT NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
    score         DOUBLE PRECISION NOT NULL,
    max_score     DOUBLE PRECISION NOT NULL,
    breakdown     JSONB,
    run_id        TEXT NOT NULL DEFAULT '',
    graded_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Durable queue state. idempotency_key keeps retries from double-creating host
-- resources (repos, collaborator grants, webhooks, ...).
CREATE TABLE IF NOT EXISTS provisioning_jobs (
    id              TEXT PRIMARY KEY,
    type            TEXT NOT NULL,
    target_ref      TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','in_progress','succeeded','failed')),
    attempts        INTEGER NOT NULL DEFAULT 0,
    idempotency_key TEXT NOT NULL UNIQUE,
    last_error      TEXT NOT NULL DEFAULT '',
    scheduled_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS grading_runs (
    id            TEXT PRIMARY KEY,
    submission_id TEXT NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
    status        TEXT NOT NULL DEFAULT 'pending',
    runner        TEXT NOT NULL DEFAULT '',
    started_at    TIMESTAMPTZ,
    finished_at   TIMESTAMPTZ,
    result        JSONB,
    logs_ref      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_roster_classroom ON roster_entries (classroom_id);
CREATE INDEX IF NOT EXISTS idx_assignments_classroom ON assignments (classroom_id);
CREATE INDEX IF NOT EXISTS idx_submissions_assignment ON submissions (assignment_id);
CREATE INDEX IF NOT EXISTS idx_jobs_status_scheduled ON provisioning_jobs (status, scheduled_at);
