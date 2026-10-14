-- A task belongs to one or more projects: position 0 is its home (repo,
-- worktree, target branch), the rest are memberships. tasks.project_id stays
-- as the denormalized home; it is written only alongside position 0 here.
CREATE TABLE task_projects (
    task_id    TEXT    NOT NULL REFERENCES tasks(id),
    project_id TEXT    NOT NULL REFERENCES projects(id),
    position   INTEGER NOT NULL,
    PRIMARY KEY (task_id, project_id),
    UNIQUE (task_id, position)
);
CREATE INDEX task_projects_by_project ON task_projects (project_id, task_id);
INSERT INTO task_projects (task_id, project_id, position) SELECT id, project_id, 0 FROM tasks;
