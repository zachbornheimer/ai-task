-- A running verification run records the process that executes it (pid, the
-- host whose pid space that is, and the process start time that tells it from
-- a later process with the same pid). A run whose owner no longer exists is
-- abandoned at once, without waiting for its heartbeat to lapse.
-- Zero/empty means the owner is unknown (runs recorded before this).
ALTER TABLE verification_runs ADD COLUMN owner_pid INTEGER NOT NULL DEFAULT 0;
ALTER TABLE verification_runs ADD COLUMN owner_host TEXT NOT NULL DEFAULT '';
ALTER TABLE verification_runs ADD COLUMN owner_start TEXT NOT NULL DEFAULT '';
