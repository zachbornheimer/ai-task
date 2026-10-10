-- Verification budgets: a task declares a size (small|medium|large) whose
-- budget caps its task checks; the project caps the regression suite.
-- 0 = the built-in default (30s / 5m / 15m; regression 2m), -1 = unlimited.
ALTER TABLE tasks ADD COLUMN size TEXT NOT NULL DEFAULT 'small';
ALTER TABLE projects ADD COLUMN budget_small_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projects ADD COLUMN budget_medium_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projects ADD COLUMN budget_large_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projects ADD COLUMN regression_budget_ms INTEGER NOT NULL DEFAULT 0;
