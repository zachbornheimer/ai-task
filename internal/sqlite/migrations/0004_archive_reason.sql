-- Archiving is a planner decision that removes a step; the reason is
-- recorded so a missing step is never silent.
ALTER TABLE tasks ADD COLUMN archive_reason TEXT NOT NULL DEFAULT '';
