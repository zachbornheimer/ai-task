-- rebuild
-- A project holds no repository, no branch and no workspace root: the first
-- two now live on the task (0020), and worktree placement is engine
-- configuration. Rebuild the table because root_path carried a UNIQUE
-- constraint, which SQLite refuses to drop a column from, and drop the
-- never-read integration column with it.
CREATE TABLE projects_new (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    plan_rev          INTEGER NOT NULL DEFAULT 0,
    max_attempts      INTEGER NOT NULL DEFAULT 5,
    retry_cooldown_ms INTEGER NOT NULL DEFAULT 0,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);

INSERT INTO projects_new (id, name, plan_rev, max_attempts, retry_cooldown_ms, created_at, updated_at)
    SELECT id, name, plan_rev, max_attempts, retry_cooldown_ms, created_at, updated_at FROM projects;

DROP TABLE projects;
ALTER TABLE projects_new RENAME TO projects;
