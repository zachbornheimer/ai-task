-- A run records whether its command ran inside the sandbox (docs/contract.md,
-- "Run result"). Runs recorded before the sandbox existed ran unconfined.
ALTER TABLE run_results ADD COLUMN sandboxed INTEGER NOT NULL DEFAULT 0;
