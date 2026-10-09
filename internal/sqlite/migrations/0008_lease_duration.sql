-- The lease each attempt was granted, so renewal extends from
-- max(current expiry, now + that lease) and a proof-of-life action never
-- shortens authority that was already granted.
ALTER TABLE execution_attempts ADD COLUMN lease_ms INTEGER NOT NULL DEFAULT 0;
