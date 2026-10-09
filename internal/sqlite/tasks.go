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

// Run is one verification run of a submission.
type Run struct {
	ID           int64
	SubmissionID int64
	Status       RunStatus
	Policy       verification.Policy
	PolicyDigest string
	Environment  string
	CreatedAt    time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
	Summary      string
}

// Submission is the stored submission fact plus its newest run.
type Submission struct {
	ID          int64
	TaskID      task.ID
	AttemptID   int64
	AttemptSeq  int
	Revision    string
	SubmittedAt time.Time
	// Run is the newest verification run; nil means none recorded yet
	// (treated as pending).
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
// Acceptance criteria are loaded separately (LoadAcceptance) because list
// views do not need them.
type Record struct {
	Task              task.Task
	UnmetDependencies int
	Attempt           *execution.Attempt // the current (fencing) attempt, if any
	Submission        *Submission        // the newest submission, if any
	CompletedAt       *time.Time
}

// Facts converts the record into the domain's status inputs.
func (r Record) Facts(now time.Time) task.Facts {
	f := task.Facts{
		Complete:          r.CompletedAt != nil,
		UnmetDependencies: r.UnmetDependencies,
	}
	if r.Attempt != nil {
		f.LeaseActive = r.Attempt.LeaseActive(now)
		f.Interrupted = r.Attempt.Interrupted(now)
	}
	if s := r.Submission; s != nil {
		switch s.RunStatus() {
		case RunPassed:
			f.AwaitingIntegration = !f.Complete
		case RunFailed, RunError:
			f.VerificationFailed = true
		default: // pending, running, or no run yet
			f.SubmissionPending = true
		}
	}
	return f
}

// Status derives the user-facing status.
func (r Record) Status(now time.Time) task.Status { return task.Derive(r.Facts(now)) }

// recordSelect is the one query shape used for every task read. The
// correlated subqueries are index-backed (task_dependencies PK, tasks PK,
// verification_runs_submission).
const recordSelect = `
SELECT t.id, t.project_id, t.description, t.outcome, t.constraints_json, t.policy_json,
       t.current_attempt_seq, t.completed_at, t.created_at, t.updated_at,
       (SELECT count(*) FROM task_dependencies d JOIN tasks r ON r.id = d.requires_id
         WHERE d.task_id = t.id AND r.completed_at IS NULL) AS unmet,
       a.id, a.seq, a.started_at, a.lease_expires_at, a.ended_at, a.end_reason,
       s.id, s.attempt_id, sa.seq, s.revision, s.submitted_at,
       vr.id, vr.status, vr.policy_json, vr.policy_digest, vr.environment, vr.created_at, vr.started_at, vr.finished_at, vr.summary
FROM tasks t
LEFT JOIN execution_attempts a ON a.task_id = t.id AND a.seq = t.current_attempt_seq
LEFT JOIN submissions s ON s.id = t.latest_submission_id
LEFT JOIN execution_attempts sa ON sa.id = s.attempt_id
LEFT JOIN verification_runs vr ON vr.id = (
    SELECT id FROM verification_runs WHERE submission_id = s.id ORDER BY id DESC LIMIT 1)
`

func scanRecord(sc interface{ Scan(...any) error }) (Record, error) {
	var r Record
	var constraints, policy string
	var completed sql.NullInt64
	var created, updated int64
	var aID, aSeq, aStarted, aExpires, aEnded sql.NullInt64
	var aReason sql.NullString
	var sID, sAttempt, sAttemptSeq, sAt sql.NullInt64
	var sRev sql.NullString
	var run runScan
	err := sc.Scan(&r.Task.ID, &r.Task.ProjectID, &r.Task.Description, &r.Task.Outcome, &constraints, &policy,
		new(int), &completed, &created, &updated, &r.UnmetDependencies,
		&aID, &aSeq, &aStarted, &aExpires, &aEnded, &aReason,
		&sID, &sAttempt, &sAttemptSeq, &sRev, &sAt,
		&run.id, &run.status, &run.policy, &run.digest, &run.env, &run.created, &run.started, &run.finished, &run.summary)
	if err != nil {
		return r, err
	}
	r.Task.CreatedAt, r.Task.UpdatedAt = fromMS(created), fromMS(updated)
	r.CompletedAt = nullMS(completed)
	if err := json.Unmarshal([]byte(constraints), &r.Task.Constraints); err != nil {
		return r, wrapInternal(err, "decode constraints")
	}
	if r.Task.Verification, err = verification.Parse([]byte(policy)); err != nil {
		return r, wrapInternal(err, "decode task policy")
	}
	if aID.Valid {
		r.Attempt = &execution.Attempt{
			ID: aID.Int64, TaskID: r.Task.ID, Seq: int(aSeq.Int64),
			StartedAt: fromMS(aStarted.Int64), LeaseExpiresAt: fromMS(aExpires.Int64),
			EndedAt: nullMS(aEnded), EndReason: execution.EndReason(aReason.String),
		}
	}
	if sID.Valid {
		sub := &Submission{
			ID: sID.Int64, TaskID: r.Task.ID, AttemptID: sAttempt.Int64, AttemptSeq: int(sAttemptSeq.Int64),
			Revision: sRev.String, SubmittedAt: fromMS(sAt.Int64),
		}
		if sub.Run, err = run.toRun(sID.Int64); err != nil {
			return r, err
		}
		r.Submission = sub
	}
	return r, nil
}

// runScan holds the nullable columns of a LEFT JOINed verification run.
type runScan struct {
	id, created, started, finished sql.NullInt64
	status, policy, digest, env    sql.NullString
	summary                        sql.NullString
}

func (rs runScan) toRun(submissionID int64) (*Run, error) {
	if !rs.id.Valid {
		return nil, nil
	}
	pol, err := verification.Parse([]byte(rs.policy.String))
	if err != nil {
		return nil, wrapInternal(err, "decode run policy")
	}
	return &Run{
		ID: rs.id.Int64, SubmissionID: submissionID, Status: RunStatus(rs.status.String), Policy: pol,
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
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO tasks (id, project_id, description, outcome, constraints_json, policy_json, policy_digest, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tk.ID, tk.ProjectID, tk.Description, tk.Outcome, string(constraints), string(policy), tk.Verification.Digest(), ms(tk.CreatedAt), ms(tk.UpdatedAt))
	if err != nil {
		if IsUniqueViolation(err) {
			return err // caller retries with a fresh ID
		}
		return wrapInternal(err, "insert task")
	}
	for i, a := range tk.Acceptance {
		if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO acceptance_criteria (task_id, position, description) VALUES (?, ?, ?)`, tk.ID, i, a.Description); err != nil {
			return wrapInternal(err, "insert acceptance criterion")
		}
	}
	return nil
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
// derives the final status; the scope only avoids scanning rows that cannot
// match.
type ListScope int

const (
	ScopeAll ListScope = iota
	// ScopeOpen: not complete.
	ScopeOpen
	// ScopeComplete: complete only.
	ScopeComplete
	// ScopeTakeable: open, no unmet prerequisite, no active lease, and no
	// submission pending or awaiting integration. Candidates for Take.
	ScopeTakeable
)

// ListRecords returns the tasks of a project in creation order.
func (t *Tx) ListRecords(pid project.ID, scope ListScope, now time.Time) ([]Record, error) {
	where, args := scopeWhere(pid, scope, now)
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

func scopeWhere(pid project.ID, scope ListScope, now time.Time) (string, []any) {
	where := ` WHERE t.project_id = ?`
	args := []any{pid}
	switch scope {
	case ScopeOpen:
		where += ` AND t.completed_at IS NULL`
	case ScopeComplete:
		where += ` AND t.completed_at IS NOT NULL`
	case ScopeTakeable:
		where += takeableWhere
		args = append(args, ms(now))
	}
	return where, args
}

// takeableWhere mirrors task.Status.Takeable in SQL. It is a pre-filter: the
// application re-derives the status from the returned facts before claiming.
const takeableWhere = `
  AND t.completed_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM task_dependencies d JOIN tasks r ON r.id = d.requires_id
                  WHERE d.task_id = t.id AND r.completed_at IS NULL)
  AND (a.id IS NULL OR a.ended_at IS NOT NULL OR a.lease_expires_at <= ?)
  AND (s.id IS NULL OR vr.status IN ('failed', 'error'))`

// TakeCandidate picks the task automatic Take should claim: interrupted work
// first, then failed verification, then fresh work, oldest first within each
// group. Returns false when nothing is takeable.
//
// Cost is one index scan over the project's open tasks (leased tasks are
// open and must be examined) plus a sort of the few takeable rows. Probing
// the three groups separately was measured slower (three scans).
func (t *Tx) TakeCandidate(pid project.ID, now time.Time) (Record, bool, error) {
	where, args := scopeWhere(pid, ScopeTakeable, now)
	// Priority mirrors task.Status.TakePriority after task.Derive: a failed
	// run outranks an expired open attempt, so it is tested first.
	order := ` ORDER BY CASE
	    WHEN vr.status IN ('failed', 'error') THEN 1
	    WHEN a.id IS NOT NULL AND a.ended_at IS NULL THEN 0
	    ELSE 2 END, t.created_at, t.id LIMIT 1`
	row := t.tx.QueryRowContext(t.ctx, recordSelect+where+order, args...)
	r, err := scanRecord(row)
	if isNoRows(err) {
		return r, false, nil
	}
	return r, err == nil, wrapInternal(err, "select candidate")
}

// TaskProject returns the owning project of a task without loading the
// record; used where only existence and ownership matter.
func (t *Tx) TaskProject(id task.ID) (project.ID, error) {
	var pid project.ID
	err := t.tx.QueryRowContext(t.ctx, `SELECT project_id FROM tasks WHERE id = ?`, id).Scan(&pid)
	if isNoRows(err) {
		return "", fault.New(fault.CodeNotFound, "task %s not found", id)
	}
	return pid, wrapInternal(err, "task project")
}

// MarkComplete records the completion fact.
func (t *Tx) MarkComplete(id task.ID, submissionID int64, now time.Time) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE tasks SET completed_at = ?, completed_submission_id = ?, updated_at = ? WHERE id = ?`, ms(now), submissionID, ms(now), id)
	return wrapInternal(err, "mark complete")
}

// ClearComplete withdraws the completion fact (used when evidence is
// invalidated). Dependents that were released by this task are not
// retroactively un-taken; see docs/invariants.md.
func (t *Tx) ClearComplete(id task.ID, now time.Time) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE tasks SET completed_at = NULL, completed_submission_id = NULL, updated_at = ? WHERE id = ?`, ms(now), id)
	return wrapInternal(err, "clear complete")
}

// CountTasks returns the number of tasks in a project (used by benchmarks
// and diagnostics).
func (t *Tx) CountTasks(pid project.ID) (int, error) {
	var n int
	err := t.tx.QueryRowContext(t.ctx, `SELECT count(*) FROM tasks WHERE project_id = ?`, pid).Scan(&n)
	return n, wrapInternal(err, "count tasks")
}

// UpdateTaskPolicy rewrites a task's verification policy.
func (t *Tx) UpdateTaskPolicy(id task.ID, policy verification.Policy, now time.Time) error {
	body, err := policy.Canonical()
	if err != nil {
		return wrapInternal(err, "encode policy")
	}
	res, err := t.tx.ExecContext(t.ctx, `UPDATE tasks SET policy_json = ?, policy_digest = ?, updated_at = ? WHERE id = ?`, string(body), policy.Digest(), ms(now), id)
	if err != nil {
		return wrapInternal(err, "update task policy")
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fault.New(fault.CodeNotFound, "task %s not found", id)
	}
	return nil
}
