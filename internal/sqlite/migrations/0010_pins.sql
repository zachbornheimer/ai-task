-- Pinned paths: files the task's branch must leave untouched; a change to
-- one fails `verify complete` as the attempt's own failure.
ALTER TABLE tasks ADD COLUMN pins_json TEXT NOT NULL DEFAULT '[]';
