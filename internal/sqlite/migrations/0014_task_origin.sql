-- Origin of a task: who filed it and from which working directory.
ALTER TABLE tasks ADD COLUMN creator TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN created_cwd TEXT NOT NULL DEFAULT '';
