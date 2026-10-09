-- Failure accounting is fenced and idempotent: at most one counted
-- failure per execution attempt, and only for the attempt that is still
-- the task's current generation. Environment and authority problems are
-- recorded as last_error for humans and handoffs, never as a strike.
ALTER TABLE tasks ADD COLUMN last_failure_attempt_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
