-- The default project used to be a blank row with id ''. It becomes a real
-- project named 'general' with a generated id; every row that pointed at ''
-- moves with it, so no task is lost.
PRAGMA defer_foreign_keys = ON;
CREATE TEMP TABLE general_rename AS
    SELECT 'proj-' || upper(hex(randomblob(8))) AS new_id WHERE EXISTS (SELECT 1 FROM projects WHERE id = '');
INSERT INTO projects (id, name, root_path, integration, regression_json, plan_rev, target_branch, workspace_root, max_attempts, retry_cooldown_ms, created_at, updated_at, budget_small_ms, budget_medium_ms, budget_large_ms, regression_budget_ms)
    SELECT g.new_id, 'general', NULL, p.integration, p.regression_json, p.plan_rev, p.target_branch, p.workspace_root, p.max_attempts, p.retry_cooldown_ms, p.created_at, p.updated_at, p.budget_small_ms, p.budget_medium_ms, p.budget_large_ms, p.regression_budget_ms
    FROM projects p, general_rename g WHERE p.id = '';
UPDATE tasks SET project_id = (SELECT new_id FROM general_rename) WHERE project_id = '' AND EXISTS (SELECT 1 FROM general_rename);
UPDATE plan_changes SET project_id = (SELECT new_id FROM general_rename) WHERE project_id = '' AND EXISTS (SELECT 1 FROM general_rename);
UPDATE verification_jobs SET project_id = (SELECT new_id FROM general_rename) WHERE project_id = '' AND EXISTS (SELECT 1 FROM general_rename);
UPDATE integration_records SET project_id = (SELECT new_id FROM general_rename) WHERE project_id = '' AND EXISTS (SELECT 1 FROM general_rename);
UPDATE integration_intents SET project_id = (SELECT new_id FROM general_rename) WHERE project_id = '' AND EXISTS (SELECT 1 FROM general_rename);
DELETE FROM projects WHERE id = '';
DROP TABLE general_rename;
