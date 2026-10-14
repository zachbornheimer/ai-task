-- A change to a claimed task's checks is recorded, never refused: the party
-- being judged must not be able to rewrite the judgment unseen.
CREATE TABLE check_history (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id    TEXT    NOT NULL REFERENCES tasks(id),
    changed_at INTEGER NOT NULL,
    field      TEXT    NOT NULL,
    old_spec   TEXT    NOT NULL,
    new_spec   TEXT    NOT NULL,
    actor      TEXT    NOT NULL,
    cwd        TEXT    NOT NULL
);
CREATE INDEX check_history_task ON check_history(task_id, id);
