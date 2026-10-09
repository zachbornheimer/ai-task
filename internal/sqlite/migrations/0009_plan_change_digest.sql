-- An idempotency key is bound to the exact request it recorded: a replay
-- with a different payload is a conflict, never a silently stale result.
ALTER TABLE plan_changes ADD COLUMN request_digest TEXT NOT NULL DEFAULT '';
