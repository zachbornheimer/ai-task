-- Integration intents make the Git promotion recoverable. Before the
-- target branch moves, the verifier records what it is about to promote
-- and under which pinned contract; after the move it records completion.
-- A process that dies in between leaves an intent whose owner lease
-- expires; reconciliation then reads Git to decide, deterministically,
-- whether the promotion happened (complete) or not (abandon).
CREATE TABLE integration_intents (
    id                 INTEGER PRIMARY KEY,
    project_id         TEXT NOT NULL REFERENCES projects(id),
    task_id            TEXT NOT NULL REFERENCES tasks(id),
    attempt_id         INTEGER REFERENCES execution_attempts(id),   -- single-task promotion
    job_id             INTEGER,                                      -- cohort promotion
    run_id             INTEGER NOT NULL,
    submission_id      INTEGER NOT NULL,
    owner_digest       TEXT NOT NULL,                                -- attempt token or job owner
    contract_rev       INTEGER NOT NULL,
    regression_digest  TEXT NOT NULL,
    target_branch      TEXT NOT NULL,
    base_revision      TEXT NOT NULL,
    source_revision    TEXT NOT NULL,
    candidate_revision TEXT NOT NULL,
    status             TEXT NOT NULL,                                -- intended | completed | abandoned
    created_at         INTEGER NOT NULL,
    finished_at        INTEGER,
    summary            TEXT NOT NULL DEFAULT ''
);
CREATE INDEX integration_intents_task ON integration_intents(task_id, id);
CREATE INDEX integration_intents_open ON integration_intents(project_id, id) WHERE status = 'intended';
