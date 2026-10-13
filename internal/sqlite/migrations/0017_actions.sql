-- A task's on-complete actions (docs/contract.md, "ActionResult"). Rows are
-- written as 'pending' in the transaction that completes the task, then
-- updated as atask runs them. run_json holds the latest RunResult.
CREATE TABLE actions (
    task_id      TEXT    NOT NULL,
    position     INTEGER NOT NULL,
    preset       TEXT    NOT NULL,
    subject      TEXT    NOT NULL,
    options_json TEXT    NOT NULL DEFAULT '{}',
    detach       INTEGER NOT NULL,
    status       TEXT    NOT NULL DEFAULT 'pending',
    attempts     INTEGER NOT NULL DEFAULT 0,
    run_json     TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (task_id, position)
);
