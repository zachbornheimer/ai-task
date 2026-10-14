-- A running verification run stamps heartbeat_at every heartbeat_every_ms
-- while its process executes; a run unseen for two intervals is dead.
-- Zero means the run predates heartbeats (the attempt lease governs it).
ALTER TABLE verification_runs ADD COLUMN heartbeat_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE verification_runs ADD COLUMN heartbeat_every_ms INTEGER NOT NULL DEFAULT 0;
