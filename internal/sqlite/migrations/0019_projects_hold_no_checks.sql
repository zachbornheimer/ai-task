-- A project holds no checks: regression and the size budgets live on the
-- task's proofs. Drop the project-level regression policy and budgets.
ALTER TABLE projects DROP COLUMN regression_json;
ALTER TABLE projects DROP COLUMN budget_small_ms;
ALTER TABLE projects DROP COLUMN budget_medium_ms;
ALTER TABLE projects DROP COLUMN budget_large_ms;
ALTER TABLE projects DROP COLUMN regression_budget_ms;
