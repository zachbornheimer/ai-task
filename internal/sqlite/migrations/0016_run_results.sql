-- Every check execution is recorded as a RunResult (docs/contract.md, "Run
-- result"): which budget governed it, whether it was killed, and the tail of
-- its output. One row per evidence row, keyed by task, attempt and run.
CREATE TABLE run_results (
    id           INTEGER PRIMARY KEY,
    task_id      TEXT    NOT NULL,
    attempt_id   INTEGER, -- NULL for an epic's own run, which has no attempt
    run_id       INTEGER NOT NULL REFERENCES verification_runs(id),
    evidence_id  INTEGER NOT NULL REFERENCES verification_results(id),
    check_type   TEXT    NOT NULL,
    preset       TEXT    NOT NULL,
    subject      TEXT    NOT NULL,
    options_json TEXT    NOT NULL DEFAULT '{}',
    started_at   INTEGER NOT NULL,
    elapsed_ms   INTEGER NOT NULL,
    budget_ms    INTEGER NOT NULL,
    timed_out    INTEGER NOT NULL,
    return_code  INTEGER,
    passed       INTEGER NOT NULL,
    output       TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX run_results_task ON run_results(task_id, id);
CREATE INDEX run_results_run ON run_results(run_id, id);
