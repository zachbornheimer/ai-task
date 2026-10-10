-- Groups planned before sizes existed (migration 0011) received the column
-- default 'small', which is too small for an epic's integration checks.
-- Give every such group that has never been verified the large budget;
-- groups with a group-mode run were created by code that knew about sizes
-- and keep what they declared.
UPDATE tasks SET size = 'large'
WHERE kind = 'group' AND size = 'small'
  AND NOT EXISTS (SELECT 1 FROM verification_runs r WHERE r.task_id = tasks.id AND r.mode = 'group');
