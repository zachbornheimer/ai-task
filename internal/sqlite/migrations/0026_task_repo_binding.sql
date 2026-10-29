-- A task, not a project, names the repository its work runs in. A project is
-- a planning scope that may span repositories, and the repository is an input
-- to verification (the commit checks run against), not an identity.
--
-- Existing tasks inherit the repository their home project was registered
-- with, so nothing changes for a board whose projects map one-to-one to
-- repositories.
ALTER TABLE tasks ADD COLUMN repo_root TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN base_ref TEXT NOT NULL DEFAULT '';

UPDATE tasks SET
    repo_root = COALESCE((SELECT p.root_path FROM projects p WHERE p.id = tasks.project_id), ''),
    base_ref  = COALESCE((SELECT p.target_branch FROM projects p WHERE p.id = tasks.project_id), '');
