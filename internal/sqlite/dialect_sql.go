package sqlite

import "strings"

// SQLite-only SQL fragments that name engine functions, with the
// PostgreSQL equivalent of each beside it. A query built from one of these
// stays byte-identical on SQLite; the PostgreSQL statement that must
// return the same facts uses the engine's own functions. This file owns
// every such fragment so no other file branches on the engine.

// recordSelectTemplate is recordSelect with a placeholder where the
// ordered-projects expression goes: it is the one column whose aggregate
// function differs by engine.
const recordSelectTemplate = `
SELECT t.id, t.project_id, t.kind, t.key, t.parent_id, t.description, t.outcome, t.constraints_json, t.policy_json,
       t.contract_rev, t.archived_at, t.archive_reason, t.failures, t.next_eligible_at,
       t.completed_at, t.created_at, t.updated_at,
       (SELECT count(*) FROM task_dependencies d JOIN tasks r ON r.id = d.requires_id
         WHERE d.task_id = t.id AND r.completed_at IS NULL) AS unmet,
       a.id, a.seq, a.started_at, a.lease_expires_at, a.ended_at, a.end_reason, a.workspace_path, a.workspace_branch, a.lease_ms,
       s.id, s.attempt_id, sa.seq, s.revision, s.submitted_at,
       vr.id, vr.attempt_id, vr.job_id, vr.mode, vr.status, vr.revision, vr.policy_json, vr.policy_digest, vr.environment, vr.created_at, vr.started_at, vr.finished_at, vr.summary,
       (SELECT revision FROM submissions WHERE id = t.completed_submission_id),
       t.completed_submission_id, t.last_failure_attempt_id, t.last_error, t.workspace_error,
       t.creator, t.created_cwd,
{PROJECTS}
FROM tasks t
LEFT JOIN execution_attempts a ON a.task_id = t.id AND a.seq = t.current_attempt_seq
LEFT JOIN submissions s ON s.id = t.latest_submission_id
LEFT JOIN execution_attempts sa ON sa.id = s.attempt_id
LEFT JOIN verification_runs vr ON vr.id = (
    SELECT id FROM verification_runs WHERE submission_id = s.id ORDER BY id DESC LIMIT 1)
`

const (
	// projectsExpr joins a task's ordered project ids with unit separators.
	projectsExpr   = `(SELECT group_concat(project_id, char(31)) FROM (SELECT project_id FROM task_projects WHERE task_id = t.id ORDER BY position))`
	pgProjectsExpr = `(SELECT string_agg(project_id, chr(31)) FROM (SELECT project_id FROM task_projects WHERE task_id = t.id ORDER BY position) p)`
	// claimableTaskChecksExpr is the EXISTS pre-filter of claimable
	// candidates: at least one required task check. The stored policy is
	// canonical JSON in a TEXT column on both engines.
	claimableTaskChecksExpr   = `AND EXISTS (SELECT 1 FROM json_each(t.policy_json, '$.task_checks') WHERE json_extract(value, '$.required') = 1)`
	pgClaimableTaskChecksExpr = `AND EXISTS (SELECT 1 FROM jsonb_array_elements(t.policy_json::jsonb -> 'task_checks') WHERE value ->> 'required' IN ('true', '1'))`
	// claimableProofExpr rejects a draft: a task that declared a proof
	// spec but no regression spec cannot be claimed. SQLite reports a
	// JSON path's type as NULL when the path is absent; PostgreSQL
	// answers key presence with ?.
	claimableProofExpr   = `AND NOT (json_type(t.policy_json, '$.proof') IS NOT NULL AND json_type(t.policy_json, '$.regression_proof') IS NULL)`
	pgClaimableProofExpr = `AND NOT (t.policy_json::jsonb ? 'proof' AND NOT t.policy_json::jsonb ? 'regression_proof')`
)

// claimableWhereTemplate is claimableWhere with placeholders for the two
// engine-specific JSON fragments.
const claimableWhereTemplate = `
  AND t.archived_at IS NULL AND t.completed_at IS NULL AND t.kind = 'task'
  AND NOT EXISTS (SELECT 1 FROM task_dependencies d JOIN tasks r ON r.id = d.requires_id
                  WHERE d.task_id = t.id AND r.completed_at IS NULL)
  AND (a.id IS NULL OR a.ended_at IS NOT NULL OR a.lease_expires_at <= ?)
  AND (s.id IS NULL OR vr.status IN ('failed', 'error'))
  AND t.next_eligible_at <= ?
  AND (? = 0 OR t.failures < ?)
  {TASK_CHECKS}
  {PROOF}
  AND t.workspace_error = ''`

// recordSelect returns the record query for this transaction's engine: the
// one query shape used for every task read.
func (t *Tx) recordSelect() string {
	projects := projectsExpr
	if t.d.isPostgres() {
		projects = pgProjectsExpr
	}
	return strings.Replace(recordSelectTemplate, "{PROJECTS}", projects, 1)
}

// claimableWhere mirrors task.Status.Claimable in SQL, for this
// transaction's engine. It is a pre-filter: the application re-derives the
// status from the returned facts before claiming.
func (t *Tx) claimableWhere() string {
	taskChecks, proof := claimableTaskChecksExpr, claimableProofExpr
	if t.d.isPostgres() {
		taskChecks, proof = pgClaimableTaskChecksExpr, pgClaimableProofExpr
	}
	return strings.NewReplacer("{TASK_CHECKS}", taskChecks, "{PROOF}", proof).Replace(claimableWhereTemplate)
}
