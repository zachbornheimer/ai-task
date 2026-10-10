-- Each check's complete output is written to a log file beside the
-- database; evidence keeps the bounded head and tail and the file's path.
ALTER TABLE verification_results ADD COLUMN log TEXT NOT NULL DEFAULT '';
