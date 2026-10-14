-- A check's size= owns its budget; the task-level size column is dropped.
-- Rule for specs stored without a size (the key is absent): before this
-- change the task size set the regression budget and a group's epic-check
-- budget, while a task's proof was always small. So the task size is copied
-- into a stored regression_proof of any task or group, and into the stored
-- proof of a group; a task's proof is left without a size and stays small.
-- Specs that already carry a size are never touched.
UPDATE tasks SET policy_json = json_set(policy_json, '$.regression_proof.size', size)
WHERE policy_json != ''
  AND json_type(policy_json, '$.regression_proof') = 'object'
  AND json_type(policy_json, '$.regression_proof.size') IS NULL;

UPDATE tasks SET policy_json = json_set(policy_json, '$.proof.size', size)
WHERE kind = 'group' AND policy_json != ''
  AND json_type(policy_json, '$.proof') = 'object'
  AND json_type(policy_json, '$.proof.size') IS NULL;

ALTER TABLE tasks DROP COLUMN size;
