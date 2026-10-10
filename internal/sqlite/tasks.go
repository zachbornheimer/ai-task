package sqlite

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// RunStatus is the stored state of a verification run.
type RunStatus string

const (
	RunPending RunStatus = "pending"
	RunRunning RunStatus = "running"
	RunPassed  RunStatus = "passed"
	RunFailed  RunStatus = "failed"
	RunError   RunStatus = "error"
	RunStale   RunStatus = "stale"
)

// Final reports whether the status will not change again.
func (s RunStatus) Final() bool {
	switch s {
	case RunPassed, RunFailed, RunError, RunStale:
		return true
	}
	return false
}

// Run is one verification run.
type Run struct {
	ID                 int64
	TaskID             task.ID
	AttemptID          int64
	SubmissionID       int64 // 0 for diagnostic runs
	JobID              int64 // cohort verifier job, if any
	Mode               verification.Mode
	Status             RunStatus
	Revision           string
	IntegratedRevision string
	Policy             verification.Policy
	PolicyDigest       string
	Environment        string
	CreatedAt          time.Time
	StartedAt          *time.Time
	FinishedAt         *time.Time
	Summary            string
}

// Submission is the stored submission fact plus its newest run.
type Submission struct {
	ID          int64
	TaskID      task.ID
	AttemptID   int64
	AttemptSeq  int
	Revision    string
	Cohort      string
	SubmittedAt time.Time
	// Run is the newest verification run of this submission; nil means
	// none recorded yet (treated as pending).
	Run *Run
}

// RunStatus returns the newest run's status, RunPending when none exists.
func (s Submission) RunStatus() RunStatus {
	if s.Run == nil {
		return RunPending
	}
	return s.Run.Status
}

// Record is a task together with every fact needed to derive its status.
// Acceptance criteria are loaded separately (LoadAcceptance).
type Record struct {
	Task              task.Task
	UnmetRequires     int
	Attempt           *execution.Attempt // the current (fencing) attempt, if any
	Submission        *Submission        // the newest submission, if any
	CompletedAt       *time.Time
	CompletedRevision string
	// CompletedSubmissionID is the submission whose run established the
	// completion fact (the newest submission may be a later, failed one).
	CompletedSubmissionID int64
	Failures              int
	NextEligibleAt        time.Time
	// LastFailureAttemptID is the attempt whose failure was counted last;
	// a second failure of the same attempt is not counted again.
	LastFailureAttemptID int64
	// LastError is the most recent environment or authority problem that
	// stopped a verification without counting as a failure.
	LastError string
	// WorkspaceError is set when the task's worktree could not be prepared
	// for a claim; the task is excluded from automatic selection until an
	// explicit claim succeeds or a planner resets it.
	WorkspaceError string
}

// Facts converts the record into the domain's status inputs.
func (r Record) Facts(now time.Time, maxAttempts int) task.Facts {
	f := task.Facts{
		Group:            r.Task.Kind == task.KindGroup,
		Archived:         r.Task.Archived(),
		Complete:         r.CompletedAt != nil,
		UnmetRequires:    r.UnmetRequires,
		Exhausted:        maxAttempts > 0 && r.Failures >= maxAttempts,
		Unverifiable:     r.Task.Kind == task.KindTask && !verification.HasRequired(r.Task.Verification.TaskChecks),
		WorkspaceBlocked: r.WorkspaceError != "",
		CooldownUntil:    r.NextEligibleAt,
		Now:              now,
	}
	if r.Attempt != nil {
		f.LeaseActive = r.Attempt.LeaseActive(now)
		f.Interrupted = r.Attempt.Interrupted(now)
	}
	if s := r.Submission; s != nil {
		switch s.RunStatus() {
		case RunPassed:
			f.AwaitingIntegration = !f.Complete
		case RunRunning:
			f.Verifying = true
		case RunFailed, RunError:
			f.VerificationFailed = true
		default: // pending or none
			f.SubmissionPending = true
		}
	}
	return f
}

// Status derives the user-facing status.
func (r Record) Status(now time.Time, maxAttempts int) task.Status {
	return task.Derive(r.Facts(now, maxAttempts))
}

// recordSelect is the one query shape used for every task read. The
// correlated subqueries are index-backed.
const recordSelect = `
SELECT t.id, t.project_id, t.kind, t.key, t.parent_id, t.description, t.outcome, t.constraints_json, t.policy_json,
       t.cohort, t.contract_rev, t.archived_at, t.archive_reason, t.failures, t.next_eligible_at,
       t.completed_at, t.created_at, t.updated_at,
       (SELECT count(*) FROM task_dependencies d JOIN tasks r ON r.id = d.requires_id
         WHERE d.task_id = t.id AND r.completed_at IS NULL) AS unmet,
       a.id, a.seq, a.started_at, a.lease_expires_at, a.ended_at, a.end_reason, a.workspace_path, a.workspace_branch, a.lease_ms,
       s.id, s.attempt_id, sa.seq, s.revision, s.cohort, s.submitted_at,
       vr.id, vr.attempt_id, vr.job_id, vr.mode, vr.status, vr.revision, vr.integrated_revision, vr.policy_json, vr.policy_digest, vr.environment, vr.created_at, vr.started_at, vr.finished_at, vr.summary,
       (SELECT revision FROM submissions WHERE id = t.completed_submission_id),
       t.completed_submission_id, t.last_failure_attempt_id, t.last_error, t.workspace_error, t.pins_json, t.size
FROM tasks t
LEFT JOIN execution_attempts a ON a.task_id = t.id AND a.seq = t.current_attempt_seq
LEFT JOIN submissions s ON s.id = t.latest_submission_id
LEFT JOIN execution_attempts sa ON sa.id = s.attempt_id
LEFT JOIN verification_runs vr ON vr.id = (
    SELECT id FROM verification_runs WHERE submission_id = s.id ORDER BY id DESC LIMIT 1)
`

func scanRecord(sc interface{ Scan(...any) error }) (Record, error) {
	var r Record
	var key, parent sql.NullString
	var constraints, policy, pins string
	var archived, completed sql.NullInt64
	var created, updated, nextEligible int64
	var aID, aSeq, aStarted, aExpires, aEnded, aLease sql.NullInt64
	var aReason, aPath, aBranch sql.NullString
	var sID, sAttempt, sAttemptSeq, sAt sql.NullInt64
	var sRev, sCohort sql.NullString
	var run runScan
	var completedRev sql.NullString
	var completedSub sql.NullInt64
	err := sc.Scan(&r.Task.ID, &r.Task.ProjectID, &r.Task.Kind, &key, &parent, &r.Task.Description, &r.Task.Outcome, &constraints, &policy,
		&r.Task.Cohort, &r.Task.ContractRev, &archived, &r.Task.ArchiveReason, &r.Failures, &nextEligible,
		&completed, &created, &updated, &r.UnmetRequires,
		&aID, &aSeq, &aStarted, &aExpires, &aEnded, &aReason, &aPath, &aBranch, &aLease,
		&sID, &sAttempt, &sAttemptSeq, &sRev, &sCohort, &sAt,
		&run.id, &run.attempt, &run.job, &run.mode, &run.status, &run.revision, &run.integrated, &run.policy, &run.digest, &run.env, &run.created, &run.started, &run.finished, &run.summary,
		&completedRev, &completedSub, &r.LastFailureAttemptID, &r.LastError, &r.WorkspaceError, &pins, &r.Task.Size)
	if err != nil {
		return r, err
	}
	r.CompletedSubmissionID = completedSub.Int64
	r.Task.Key, r.Task.ParentID = key.String, task.ID(parent.String)
	r.Task.ArchivedAt = nullMS(archived)
	r.Task.CreatedAt, r.Task.UpdatedAt = fromMS(created), fromMS(updated)
	r.CompletedAt = nullMS(completed)
	r.CompletedRevision = completedRev.String
	if nextEligible > 0 {
		r.NextEligibleAt = fromMS(nextEligible)
	}
	if err := json.Unmarshal([]byte(constraints), &r.Task.Constraints); err != nil {
		return r, wrapInternal(err, "decode constraints")
	}
	if err := json.Unmarshal([]byte(pins), &r.Task.Pins); err != nil {
		return r, wrapInternal(err, "decode pins")
	}
	if r.Task.Verification, err = verification.Parse([]byte(policy)); err != nil {
		return r, wrapInternal(err, "decode task policy")
	}
	if aID.Valid {
		r.Attempt = &execution.Attempt{
			ID: aID.Int64, TaskID: r.Task.ID, Seq: int(aSeq.Int64),
			StartedAt: fromMS(aStarted.Int64), LeaseExpiresAt: fromMS(aExpires.Int64),
			EndedAt: nullMS(aEnded), EndReason: execution.EndReason(aReason.String),
			WorkspacePath: aPath.String, WorkspaceBranch: aBranch.String,
			Lease: time.Duration(aLease.Int64) * time.Millisecond,
		}
	}
	if sID.Valid {
		sub := &Submission{
			ID: sID.Int64, TaskID: r.Task.ID, AttemptID: sAttempt.Int64, AttemptSeq: int(sAttemptSeq.Int64),
			Revision: sRev.String, Cohort: sCohort.String, SubmittedAt: fromMS(sAt.Int64),
		}
		if sub.Run, err = run.toRun(r.Task.ID, sID.Int64); err != nil {
			return r, err
		}
		r.Submission = sub
	}
	return r, nil
}

// runScan holds the nullable columns of a LEFT JOINed verification run.
type runScan struct {
	id, attempt, job, created, started, finished sql.NullInt64
	mode, status, revision, integrated           sql.NullString
	policy, digest, env, summary                 sql.NullString
}

func (rs runScan) toRun(taskID task.ID, submissionID int64) (*Run, error) {
	if !rs.id.Valid {
		return nil, nil
	}
	pol, err := verification.Parse([]byte(rs.policy.String))
	if err != nil {
		return nil, wrapInternal(err, "decode run policy")
	}
	return &Run{
		ID: rs.id.Int64, TaskID: taskID, AttemptID: rs.attempt.Int64, SubmissionID: submissionID, JobID: rs.job.Int64,
		Mode: verification.Mode(rs.mode.String), Status: RunStatus(rs.status.String),
		Revision: rs.revision.String, IntegratedRevision: rs.integrated.String, Policy: pol,
		PolicyDigest: rs.digest.String, Environment: rs.env.String, CreatedAt: fromMS(rs.created.Int64),
		StartedAt: nullMS(rs.started), FinishedAt: nullMS(rs.finished), Summary: rs.summary.String,
	}, nil
}

// InsertTask stores a validated task and its acceptance criteria.
func (t *Tx) InsertTask(tk task.Task) error {
	constraints, err := json.Marshal(nonNil(tk.Constraints))
	if err != nil {
		return wrapInternal(err, "encode constraints")
	}
	policy, err := tk.Verification.Canonical()
	if err != nil {
		return wrapInternal(err, "encode policy")
	}
	var key, parent any
	if tk.Key != "" {
		key = tk.Key
	}
	if tk.ParentID != "" {
		parent = tk.ParentID
	}
	pins, err := json.Marshal(nonNil(tk.Pins))
	if err != nil {
		return wrapInternal(err, "encode pins")
	}
	size := tk.Size
	if size == "" {
		size = task.SizeSmall
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO tasks (id, project_id, kind, key, parent_id, description, outcome, constraints_json, policy_json, policy_digest, cohort, contract_rev, created_at, updated_at, pins_json, size)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`,
		tk.ID, tk.ProjectID, tk.Kind, key, parent, tk.Description, tk.Outcome, string(constraints), string(policy), tk.Verification.Digest(), tk.Cohort, ms(tk.CreatedAt), ms(tk.UpdatedAt), string(pins), size)
	if err != nil {
		if IsUniqueViolation(err) {
			return err // caller distinguishes id vs key collisions
		}
		return wrapInternal(err, "insert task")
	}
	return t.replaceAcceptance(tk.ID, tk.Acceptance)
}

func (t *Tx) replaceAcceptance(id task.ID, acceptance []task.AcceptanceCriterion) error {
	if _, err := t.tx.ExecContext(t.ctx, `DELETE FROM acceptance_criteria WHERE task_id = ?`, id); err != nil {
		return wrapInternal(err, "clear acceptance criteria")
	}
	for i, a := range acceptance {
		if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO acceptance_criteria (task_id, position, description) VALUES (?, ?, ?)`, id, i, a.Description); err != nil {
			return wrapInternal(err, "insert acceptance criterion")
		}
	}
	return nil
}

// UpdateTaskContract rewrites the mutable contract fields of a task and
// bumps its contract revision.
func (t *Tx) UpdateTaskContract(tk task.Task, now time.Time) error {
	constraints, err := json.Marshal(nonNil(tk.Constraints))
	if err != nil {
		return wrapInternal(err, "encode constraints")
	}
	policy, err := tk.Verification.Canonical()
	if err != nil {
		return wrapInternal(err, "encode policy")
	}
	var parent any
	if tk.ParentID != "" {
		parent = tk.ParentID
	}
	pins, err := json.Marshal(nonNil(tk.Pins))
	if err != nil {
		return wrapInternal(err, "encode pins")
	}
	size := tk.Size
	if size == "" {
		size = task.SizeSmall
	}
	res, err := t.tx.ExecContext(t.ctx, `UPDATE tasks SET parent_id = ?, description = ?, outcome = ?, constraints_json = ?, policy_json = ?, policy_digest = ?, cohort = ?, pins_json = ?, size = ?, contract_rev = contract_rev + 1, updated_at = ? WHERE id = ?`,
		parent, tk.Description, tk.Outcome, string(constraints), string(policy), tk.Verification.Digest(), tk.Cohort, string(pins), size, ms(now), tk.ID)
	if err != nil {
		return wrapInternal(err, "update task")
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fault.New(fault.CodeNotFound, "task %s not found", tk.ID)
	}
	return t.replaceAcceptance(tk.ID, tk.Acceptance)
}

// ArchiveTask soft-deletes a task.
func (t *Tx) ArchiveTask(id task.ID, reason string, now time.Time) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE tasks SET archived_at = ?, archive_reason = ?, updated_at = ? WHERE id = ?`, ms(now), reason, ms(now), id)
	return wrapInternal(err, "archive task")
}

// ResetAttempts clears failure bookkeeping.
func (t *Tx) ResetAttempts(id task.ID, now time.Time) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE tasks SET failures = 0, next_eligible_at = 0, last_failure_attempt_id = 0, last_error = '', workspace_error = '', updated_at = ? WHERE id = ?`, ms(now), id)
	return wrapInternal(err, "reset attempts")
}

// SetWorkspaceError records why the task's worktree could not be
// prepared ("" clears it).
func (t *Tx) SetWorkspaceError(id task.ID, msg string, now time.Time) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE tasks SET workspace_error = ?, updated_at = ? WHERE id = ?`, msg, ms(now), id)
	return wrapInternal(err, "set workspace error")
}

// RecordFailure counts one failure of an attempt and sets the cooldown.
// It is fenced (the attempt must still be the task's current generation
// and the task must not be complete) and idempotent (at most one counted
// failure per attempt). It reports whether a failure was counted.
func (t *Tx) RecordFailure(id task.ID, attemptID int64, cooldown time.Duration, now time.Time) (bool, error) {
	next := int64(0)
	if cooldown > 0 {
		next = ms(now.Add(cooldown))
	}
	res, err := t.tx.ExecContext(t.ctx, `UPDATE tasks SET failures = failures + 1, last_failure_attempt_id = ?, next_eligible_at = ?, updated_at = ?
		WHERE id = ? AND completed_at IS NULL AND last_failure_attempt_id <> ?
		  AND current_attempt_seq = (SELECT seq FROM execution_attempts WHERE id = ? AND task_id = tasks.id)`,
		attemptID, next, ms(now), id, attemptID, attemptID)
	if err != nil {
		return false, wrapInternal(err, "record failure")
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SetLastError records an environment or authority problem on the task
// without counting a failure. It is fenced to the current attempt so a
// stale verifier cannot overwrite a successor's state.
func (t *Tx) SetLastError(id task.ID, attemptID int64, msg string, now time.Time) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE tasks SET last_error = ?, updated_at = ? WHERE id = ? AND completed_at IS NULL
		AND (? = 0 OR current_attempt_seq = (SELECT seq FROM execution_attempts WHERE id = ? AND task_id = tasks.id))`, msg, ms(now), id, attemptID, attemptID)
	return wrapInternal(err, "set last error")
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// GetRecord loads one task with its status facts.
func (t *Tx) GetRecord(id task.ID) (Record, error) {
	row := t.tx.QueryRowContext(t.ctx, recordSelect+` WHERE t.id = ?`, id)
	r, err := scanRecord(row)
	if isNoRows(err) {
		return r, fault.New(fault.CodeNotFound, "task %s not found", id)
	}
	return r, wrapInternal(err, "get task")
}

// GetByKey loads a task by its stable key within a project.
func (t *Tx) GetByKey(pid project.ID, key string) (Record, bool, error) {
	row := t.tx.QueryRowContext(t.ctx, recordSelect+` WHERE t.project_id = ? AND t.key = ?`, pid, key)
	r, err := scanRecord(row)
	if isNoRows(err) {
		return r, false, nil
	}
	return r, err == nil, wrapInternal(err, "get task by key")
}

// LoadAcceptance fills in the acceptance criteria of a task.
func (t *Tx) LoadAcceptance(tk *task.Task) error {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT description FROM acceptance_criteria WHERE task_id = ? ORDER BY position`, tk.ID)
	if err != nil {
		return wrapInternal(err, "load acceptance criteria")
	}
	defer rows.Close()
	tk.Acceptance = nil
	for rows.Next() {
		var a task.AcceptanceCriterion
		if err := rows.Scan(&a.Description); err != nil {
			return wrapInternal(err, "scan criterion")
		}
		tk.Acceptance = append(tk.Acceptance, a)
	}
	return rows.Err()
}

// ListScope narrows a project listing at the SQL level. The domain still
// derives the final status; the scope only avoids scanning rows that
// cannot match.
type ListScope int

const (
	// ScopeAll: every task and group, archived included.
	ScopeAll ListScope = iota
	// ScopeLive: not archived.
	ScopeLive
	// ScopeOpen: not archived, not complete.
	ScopeOpen
	// ScopeClaimable: open executable tasks with no unmet prerequisite, no
	// live lease, no submission pending or passed, no cooldown, and
	// failures below the limit. Candidates for Claim.
	ScopeClaimable
)

// ListRecords returns the tasks of a project in creation order.
func (t *Tx) ListRecords(pid project.ID, scope ListScope, now time.Time, maxAttempts int) ([]Record, error) {
	where, args := scopeWhere(pid, scope, now, maxAttempts)
	rows, err := t.tx.QueryContext(t.ctx, recordSelect+where+` ORDER BY t.created_at, t.id`, args...)
	if err != nil {
		return nil, wrapInternal(err, "list tasks")
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, wrapInternal(err, "scan task")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scopeWhere(pid project.ID, scope ListScope, now time.Time, maxAttempts int) (string, []any) {
	where := ` WHERE t.project_id = ?`
	args := []any{pid}
	switch scope {
	case ScopeLive:
		where += ` AND t.archived_at IS NULL`
	case ScopeOpen:
		where += ` AND t.archived_at IS NULL AND t.completed_at IS NULL`
	case ScopeClaimable:
		where += claimableWhere
		args = append(args, ms(now), ms(now), maxAttempts, maxAttempts)
	}
	return where, args
}

// claimableWhere mirrors task.Status.Claimable in SQL. It is a pre-filter:
// the application re-derives the status from the returned facts before
// claiming.
const claimableWhere = `
  AND t.archived_at IS NULL AND t.completed_at IS NULL AND t.kind = 'task'
  AND NOT EXISTS (SELECT 1 FROM task_dependencies d JOIN tasks r ON r.id = d.requires_id
                  WHERE d.task_id = t.id AND r.completed_at IS NULL)
  AND (a.id IS NULL OR a.ended_at IS NOT NULL OR a.lease_expires_at <= ?)
  AND (s.id IS NULL OR vr.status IN ('failed', 'error'))
  AND t.next_eligible_at <= ?
  AND (? = 0 OR t.failures < ?)
  AND EXISTS (SELECT 1 FROM json_each(t.policy_json, '$.task_checks') WHERE json_extract(value, '$.required') = 1)
  AND t.workspace_error = ''`

// ClaimCandidate picks the task automatic Claim should take: the oldest
// claimable task, ties broken by ID. Deterministic; no priority field.
func (t *Tx) ClaimCandidate(pid project.ID, now time.Time, maxAttempts int) (Record, bool, error) {
	where, args := scopeWhere(pid, ScopeClaimable, now, maxAttempts)
	row := t.tx.QueryRowContext(t.ctx, recordSelect+where+` ORDER BY t.created_at, t.id LIMIT 1`, args...)
	r, err := scanRecord(row)
	if isNoRows(err) {
		return r, false, nil
	}
	return r, err == nil, wrapInternal(err, "select candidate")
}

// TaskProject returns the owning project and kind of a task without
// loading the record.
func (t *Tx) TaskProject(id task.ID) (project.ID, task.Kind, bool, error) {
	var pid project.ID
	var kind task.Kind
	var archived sql.NullInt64
	err := t.tx.QueryRowContext(t.ctx, `SELECT project_id, kind, archived_at FROM tasks WHERE id = ?`, id).Scan(&pid, &kind, &archived)
	if isNoRows(err) {
		return "", "", false, fault.New(fault.CodeNotFound, "task %s not found", id)
	}
	return pid, kind, archived.Valid, wrapInternal(err, "task project")
}

// MarkComplete records the completion fact.
func (t *Tx) MarkComplete(id task.ID, submissionID int64, now time.Time) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE tasks SET completed_at = ?, completed_submission_id = ?, last_error = '', updated_at = ? WHERE id = ?`, ms(now), submissionID, ms(now), id)
	return wrapInternal(err, "mark complete")
}

// MarkGroupComplete records that a group's own checks passed.
func (t *Tx) MarkGroupComplete(id task.ID, now time.Time) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE tasks SET completed_at = ?, last_error = '', updated_at = ? WHERE id = ? AND kind = 'group'`, ms(now), ms(now), id)
	return wrapInternal(err, "mark group complete")
}

// ClearComplete withdraws the completion fact.
func (t *Tx) ClearComplete(id task.ID, now time.Time) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE tasks SET completed_at = NULL, completed_submission_id = NULL, updated_at = ? WHERE id = ?`, ms(now), id)
	return wrapInternal(err, "clear complete")
}

// Children returns the direct members of a group (live only), oldest first.
func (t *Tx) Children(id task.ID) ([]Record, error) {
	rows, err := t.tx.QueryContext(t.ctx, recordSelect+` WHERE t.parent_id = ? AND t.archived_at IS NULL ORDER BY t.created_at, t.id`, id)
	if err != nil {
		return nil, wrapInternal(err, "list children")
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, wrapInternal(err, "scan child")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ParentChainContains reports whether `ancestor` is `id` or one of its
// parents, used to keep the group hierarchy a tree.
func (t *Tx) ParentChainContains(id, ancestor task.ID) (bool, error) {
	cur := id
	for i := 0; i < 1000 && cur != ""; i++ {
		if cur == ancestor {
			return true, nil
		}
		var parent sql.NullString
		err := t.tx.QueryRowContext(t.ctx, `SELECT parent_id FROM tasks WHERE id = ?`, cur).Scan(&parent)
		if isNoRows(err) {
			return false, nil
		}
		if err != nil {
			return false, wrapInternal(err, "parent chain")
		}
		cur = task.ID(parent.String)
	}
	return false, nil
}

// CountTasks returns the number of tasks in a project.
func (t *Tx) CountTasks(pid project.ID) (int, error) {
	var n int
	err := t.tx.QueryRowContext(t.ctx, `SELECT count(*) FROM tasks WHERE project_id = ?`, pid).Scan(&n)
	return n, wrapInternal(err, "count tasks")
}

// CohortMembers returns live, incomplete executable tasks of a cohort.
func (t *Tx) CohortMembers(pid project.ID, cohort string) ([]Record, error) {
	rows, err := t.tx.QueryContext(t.ctx, recordSelect+` WHERE t.project_id = ? AND t.cohort = ? AND t.archived_at IS NULL AND t.completed_at IS NULL AND t.kind = 'task' ORDER BY t.created_at, t.id`, pid, cohort)
	if err != nil {
		return nil, wrapInternal(err, "cohort members")
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, wrapInternal(err, "scan member")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TaskCheckIDs returns every task-check ID used by live tasks in a project,
// so a regression policy change can reject collisions.
func (t *Tx) TaskCheckIDs(pid project.ID) (map[string]task.ID, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT id, policy_json FROM tasks WHERE project_id = ? AND archived_at IS NULL AND policy_json <> ''`, pid)
	if err != nil {
		return nil, wrapInternal(err, "task check ids")
	}
	defer rows.Close()
	out := map[string]task.ID{}
	for rows.Next() {
		var id task.ID
		var body string
		if err := rows.Scan(&id, &body); err != nil {
			return nil, wrapInternal(err, "scan policy")
		}
		p, err := verification.Parse([]byte(body))
		if err != nil {
			return nil, err
		}
		for _, c := range p.TaskChecks {
			out[c.ID] = id
		}
	}
	return out, rows.Err()
}
