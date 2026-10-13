-- The task-level verify mode is now called proof.
UPDATE verification_runs SET mode = 'proof' WHERE mode = 'task';
