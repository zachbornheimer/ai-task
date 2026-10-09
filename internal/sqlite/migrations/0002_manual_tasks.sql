-- Manual tasks require a human action. They participate in the graph and
-- derive status like any other task, but automatic selection (take with no
-- task id) never claims them; a human (or a deliberate explicit take) does.
ALTER TABLE tasks ADD COLUMN manual INTEGER NOT NULL DEFAULT 0;
