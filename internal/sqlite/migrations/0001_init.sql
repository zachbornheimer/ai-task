-- Milestone 1 schema. Timestamps are integer Unix milliseconds (UTC).
-- Status is never stored: every user-facing state is derived from the facts
-- in these tables (see docs/invariants.md).

CREATE TABLE projects (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    root_path        TEXT UNIQUE,                 -- NULL: project has no directory
    integration      TEXT NOT NULL DEFAULT 'none',
    regression_json  TEXT NOT NULL DEFAULT '',    -- canonical verification policy JSON
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);

CREATE TABLE tasks (
    id                       TEXT PRIMARY KEY,
    project_id               TEXT NOT NULL REFERENCES projects(id),
    description              TEXT NOT NULL,
    outcome                  TEXT NOT NULL,
    constraints_json         TEXT NOT NULL DEFAULT '[]',
    policy_json              TEXT NOT NULL DEFAULT '',
    policy_digest            TEXT NOT NULL,
    -- Fencing generation: the attempt with seq = current_attempt_seq is the
    -- only one that can hold authority. 0 = never taken.
    current_attempt_seq      INTEGER NOT NULL DEFAULT 0,
    -- Engine-maintained pointer to the newest submission (never user-edited).
    latest_submission_id     INTEGER,
    -- Completion fact: set in the same transaction that records the final
    -- passing gate; NULL otherwise.
    completed_at             INTEGER,
    completed_submission_id  INTEGER,
    created_at               INTEGER NOT NULL,
    updated_at               INTEGER NOT NULL
);
CREATE INDEX tasks_project_created ON tasks(project_id, created_at, id);
CREATE INDEX tasks_project_open ON tasks(project_id, created_at, id) WHERE completed_at IS NULL;

CREATE TABLE acceptance_criteria (
    task_id     TEXT NOT NULL REFERENCES tasks(id),
    position    INTEGER NOT NULL,
    description TEXT NOT NULL,
    PRIMARY KEY (task_id, position)
);

-- task_id requires requires_id. One row per edge; the primary key rejects
-- duplicates and the CHECK rejects self-dependency at the storage layer too.
CREATE TABLE task_dependencies (
    task_id     TEXT NOT NULL REFERENCES tasks(id),
    requires_id TEXT NOT NULL REFERENCES tasks(id),
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (task_id, requires_id),
    CHECK (task_id <> requires_id)
);
CREATE INDEX task_dependencies_requires ON task_dependencies(requires_id, task_id);

CREATE TABLE execution_attempts (
    id               INTEGER PRIMARY KEY,
    task_id          TEXT NOT NULL REFERENCES tasks(id),
    seq              INTEGER NOT NULL,
    token_digest     TEXT NOT NULL UNIQUE,         -- SHA-256 of the session token
    started_at       INTEGER NOT NULL,
    lease_expires_at INTEGER NOT NULL,
    ended_at         INTEGER,
    end_reason       TEXT,                         -- finished | superseded
    UNIQUE (task_id, seq)
);

CREATE TABLE task_log_entries (
    id          INTEGER PRIMARY KEY,               -- global ordering sequence
    task_id     TEXT NOT NULL REFERENCES tasks(id),
    attempt_id  INTEGER NOT NULL REFERENCES execution_attempts(id),
    done        TEXT NOT NULL DEFAULT '',
    next        TEXT NOT NULL DEFAULT '',
    learned     TEXT NOT NULL DEFAULT '',
    note        TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);
CREATE INDEX task_log_entries_task ON task_log_entries(task_id, id);

-- A submission is the immutable fact "attempt X submitted revision R". At
-- most one per attempt. revision is '' when the project has no Git tree.
CREATE TABLE submissions (
    id             INTEGER PRIMARY KEY,
    task_id        TEXT NOT NULL REFERENCES tasks(id),
    attempt_id     INTEGER NOT NULL UNIQUE REFERENCES execution_attempts(id),
    revision       TEXT NOT NULL DEFAULT '',
    submitted_at   INTEGER NOT NULL
);
CREATE INDEX submissions_task ON submissions(task_id, id);

-- One row per attempt to verify a submission under one effective policy
-- (task checks + project regression, digested). Retries and policy changes
-- append rows; the newest row is the submission's verification state.
--   pending  - recorded, checks not started
--   running  - a process is (or was, if it crashed) executing checks
--   passed / failed / error - final
--   stale    - the task policy changed after this run; evidence no longer applies
CREATE TABLE verification_runs (
    id             INTEGER PRIMARY KEY,
    submission_id  INTEGER NOT NULL REFERENCES submissions(id),
    status         TEXT NOT NULL,
    policy_json    TEXT NOT NULL DEFAULT '',
    policy_digest  TEXT NOT NULL,
    environment    TEXT NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    started_at     INTEGER,
    finished_at    INTEGER,
    summary        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX verification_runs_submission ON verification_runs(submission_id, id);

-- Evidence: what actually ran (or was reused), on which inputs, with what
-- result. Rows are immutable. Each row commits in its own transaction as
-- soon as its check finishes so a crash mid-run loses at most one check.
CREATE TABLE verification_results (
    id             INTEGER PRIMARY KEY,
    run_id         INTEGER NOT NULL REFERENCES verification_runs(id),
    check_id       TEXT NOT NULL,
    check_version  TEXT NOT NULL DEFAULT '',
    check_digest   TEXT NOT NULL,
    required       INTEGER NOT NULL,
    revision       TEXT NOT NULL DEFAULT '',
    policy_digest  TEXT NOT NULL,
    started_at     INTEGER NOT NULL,
    finished_at    INTEGER NOT NULL,
    exit_code      INTEGER NOT NULL,
    outcome        TEXT NOT NULL,                   -- passed | failed | timeout | error | skipped
    reused         INTEGER NOT NULL DEFAULT 0,
    reused_from    INTEGER,
    message        TEXT NOT NULL DEFAULT '',
    stdout         TEXT NOT NULL DEFAULT '',
    stderr         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX verification_results_run ON verification_results(run_id, id);
-- Reuse lookup: same check content at the same revision.
CREATE INDEX verification_results_reuse ON verification_results(check_digest, revision, outcome) WHERE reused = 0;
