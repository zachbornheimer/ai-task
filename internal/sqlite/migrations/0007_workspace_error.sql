-- A task whose worktree could not be prepared is excluded from automatic
-- selection (needs_attention with this reason) so one broken workspace
-- cannot starve the queue; an explicit claim retries, a planner reset
-- clears it.
ALTER TABLE tasks ADD COLUMN workspace_error TEXT NOT NULL DEFAULT '';
