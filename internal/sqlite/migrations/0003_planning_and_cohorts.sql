-- Planning API, groups, archive, cohorts, retry policy, per-attempt
-- workspaces, cohort verification jobs, integration records.

-- Projects: plan revision for optimistic concurrency, target branch and
-- workspace root for Git projects, retry policy.
ALTER TABLE projects ADD COLUMN plan_rev INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projects ADD COLUMN target_branch TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN workspace_root TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN max_attempts INTEGER NOT NULL DEFAULT 5;
ALTER TABLE projects ADD COLUMN retry_cooldown_ms INTEGER NOT NULL DEFAULT 0;

-- Tasks: stable caller keys, organizational groups, cohorts, archive,
-- retry bookkeeping.
ALTER TABLE tasks ADD COLUMN key TEXT;
ALTER TABLE tasks ADD COLUMN kind TEXT NOT NULL DEFAULT 'task';   -- task | group
ALTER TABLE tasks ADD COLUMN parent_id TEXT REFERENCES tasks(id);
ALTER TABLE tasks ADD COLUMN cohort TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN archived_at INTEGER;
ALTER TABLE tasks ADD COLUMN failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN next_eligible_at INTEGER NOT NULL DEFAULT 0;
-- Bumped on every contract change so a run can detect a policy edit.
ALTER TABLE tasks ADD COLUMN contract_rev INTEGER NOT NULL DEFAULT 1;
CREATE UNIQUE INDEX tasks_project_key ON tasks(project_id, key) WHERE key IS NOT NULL;
CREATE INDEX tasks_parent ON tasks(parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX tasks_cohort ON tasks(project_id, cohort) WHERE cohort <> '';

-- Idempotent plan changes: a replayed ChangeSet returns its stored result.
CREATE TABLE plan_changes (
    project_id      TEXT NOT NULL REFERENCES projects(id),
    idempotency_key TEXT NOT NULL,
    plan_rev        INTEGER NOT NULL,
    result_json     TEXT NOT NULL,
    applied_at      INTEGER NOT NULL,
    PRIMARY KEY (project_id, idempotency_key)
);

-- Attempts own a workspace (per-attempt Git worktree) when the project is
-- a repository.
ALTER TABLE execution_attempts ADD COLUMN workspace_path TEXT NOT NULL DEFAULT '';
ALTER TABLE execution_attempts ADD COLUMN workspace_branch TEXT NOT NULL DEFAULT '';

-- Submissions are per (attempt, revision): a failed final verification keeps
-- the claim live and the next verify complete records a new submission.
-- The UNIQUE(attempt_id) constraint from 0001 is replaced by a new table.
CREATE TABLE submissions_v2 (
    id             INTEGER PRIMARY KEY,
    task_id        TEXT NOT NULL REFERENCES tasks(id),
    attempt_id     INTEGER NOT NULL REFERENCES execution_attempts(id),
    revision       TEXT NOT NULL DEFAULT '',
    cohort         TEXT NOT NULL DEFAULT '',
    submitted_at   INTEGER NOT NULL
);
INSERT INTO submissions_v2 (id, task_id, attempt_id, revision, submitted_at)
    SELECT id, task_id, attempt_id, revision, submitted_at FROM submissions;
DROP TABLE submissions;
ALTER TABLE submissions_v2 RENAME TO submissions;
CREATE INDEX submissions_task ON submissions(task_id, id);
CREATE INDEX submissions_attempt ON submissions(attempt_id, id);

-- Runs gain a mode (task | regression | complete | cohort), the revision
-- they judged, the integrated revision when promotion changed it, and the
-- attempt/task they belong to. Diagnostic runs (task, regression) have no
-- submission, so submission_id becomes nullable: the table is rebuilt.
CREATE TABLE verification_runs_v2 (
    id                  INTEGER PRIMARY KEY,
    task_id             TEXT NOT NULL REFERENCES tasks(id),
    attempt_id          INTEGER REFERENCES execution_attempts(id),
    submission_id       INTEGER REFERENCES submissions(id),
    job_id              INTEGER,
    mode                TEXT NOT NULL DEFAULT 'complete',
    status              TEXT NOT NULL,
    revision            TEXT NOT NULL DEFAULT '',
    integrated_revision TEXT NOT NULL DEFAULT '',
    policy_json         TEXT NOT NULL DEFAULT '',
    policy_digest       TEXT NOT NULL,
    environment         TEXT NOT NULL DEFAULT '',
    created_at          INTEGER NOT NULL,
    started_at          INTEGER,
    finished_at         INTEGER,
    summary             TEXT NOT NULL DEFAULT ''
);
INSERT INTO verification_runs_v2 (id, task_id, attempt_id, submission_id, status, revision, policy_json, policy_digest, environment, created_at, started_at, finished_at, summary)
    SELECT r.id, s.task_id, s.attempt_id, r.submission_id, r.status, s.revision, r.policy_json, r.policy_digest, r.environment, r.created_at, r.started_at, r.finished_at, r.summary
    FROM verification_runs r JOIN submissions s ON s.id = r.submission_id;
DROP TABLE verification_runs;
ALTER TABLE verification_runs_v2 RENAME TO verification_runs;
CREATE INDEX verification_runs_submission ON verification_runs(submission_id, id);
CREATE INDEX verification_runs_task ON verification_runs(task_id, id);
CREATE INDEX verification_runs_job ON verification_runs(job_id) WHERE job_id IS NOT NULL;

-- Cohort verification jobs: durable, leased, recoverable.
CREATE TABLE verification_jobs (
    id               INTEGER PRIMARY KEY,
    project_id       TEXT NOT NULL REFERENCES projects(id),
    cohort           TEXT NOT NULL,
    status           TEXT NOT NULL,              -- running | passed | failed | error
    members_json     TEXT NOT NULL,              -- [{task_id, submission_id, revision}]
    owner_digest     TEXT NOT NULL DEFAULT '',
    lease_expires_at INTEGER NOT NULL DEFAULT 0,
    candidate        TEXT NOT NULL DEFAULT '',   -- assembled candidate revision
    created_at       INTEGER NOT NULL,
    finished_at      INTEGER,
    summary          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX verification_jobs_cohort ON verification_jobs(project_id, cohort, id);

-- Integration records: what was promoted where.
CREATE TABLE integration_records (
    id              INTEGER PRIMARY KEY,
    project_id      TEXT NOT NULL REFERENCES projects(id),
    task_id         TEXT REFERENCES tasks(id),
    job_id          INTEGER,
    target_branch   TEXT NOT NULL,
    base_revision   TEXT NOT NULL,
    source_revision TEXT NOT NULL,
    result_revision TEXT NOT NULL,
    created_at      INTEGER NOT NULL
);
