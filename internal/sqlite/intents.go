package sqlite

import (
	"database/sql"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// Intent statuses.
const (
	IntentIntended  = "intended"
	IntentCompleted = "completed"
	IntentAbandoned = "abandoned"
)

// Intent is a durable record of a promotion that is about to happen (or
// did): what is promoted, onto what, under which pinned contract, and who
// owns the operation. One row per task; cohort promotions write one row
// per member sharing a job id and candidate.
type Intent struct {
	ID                int64      `json:"id"`
	ProjectID         project.ID `json:"project_id"`
	TaskID            task.ID    `json:"task_id"`
	AttemptID         int64      `json:"attempt_id,omitempty"`
	JobID             int64      `json:"job_id,omitempty"`
	RunID             int64      `json:"run_id"`
	SubmissionID      int64      `json:"submission_id"`
	OwnerDigest       string     `json:"-"`
	ContractRev       int        `json:"contract_rev"`
	RegressionDigest  string     `json:"regression_digest"`
	TargetBranch      string     `json:"target_branch"`
	BaseRevision      string     `json:"base_revision"`
	SourceRevision    string     `json:"source_revision"`
	CandidateRevision string     `json:"candidate_revision"`
	Status            string     `json:"status"`
	CreatedAt         time.Time  `json:"created_at"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
	Summary           string     `json:"summary,omitempty"`
}

// InsertIntent records an intended promotion.
func (t *Tx) InsertIntent(i Intent, now time.Time) (int64, error) {
	var attempt, job any
	if i.AttemptID != 0 {
		attempt = i.AttemptID
	}
	if i.JobID != 0 {
		job = i.JobID
	}
	res, err := t.tx.ExecContext(t.ctx, `INSERT INTO integration_intents
		(project_id, task_id, attempt_id, job_id, run_id, submission_id, owner_digest, contract_rev, regression_digest, target_branch, base_revision, source_revision, candidate_revision, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'intended', ?)`,
		i.ProjectID, i.TaskID, attempt, job, i.RunID, i.SubmissionID, i.OwnerDigest, i.ContractRev, i.RegressionDigest, i.TargetBranch, i.BaseRevision, i.SourceRevision, i.CandidateRevision, ms(now))
	if err != nil {
		return 0, wrapInternal(err, "insert integration intent")
	}
	id, _ := res.LastInsertId()
	return id, nil
}

const intentColumns = `id, project_id, task_id, attempt_id, job_id, run_id, submission_id, owner_digest, contract_rev, regression_digest, target_branch, base_revision, source_revision, candidate_revision, status, created_at, finished_at, summary`

func scanIntent(sc interface{ Scan(...any) error }) (Intent, error) {
	var i Intent
	var attempt, job, finished sql.NullInt64
	var created int64
	if err := sc.Scan(&i.ID, &i.ProjectID, &i.TaskID, &attempt, &job, &i.RunID, &i.SubmissionID, &i.OwnerDigest, &i.ContractRev, &i.RegressionDigest, &i.TargetBranch, &i.BaseRevision, &i.SourceRevision, &i.CandidateRevision, &i.Status, &created, &finished, &i.Summary); err != nil {
		return i, err
	}
	i.AttemptID, i.JobID, i.CreatedAt, i.FinishedAt = attempt.Int64, job.Int64, fromMS(created), nullMS(finished)
	return i, nil
}

// GetIntent loads one intent.
func (t *Tx) GetIntent(id int64) (Intent, error) {
	i, err := scanIntent(t.tx.QueryRowContext(t.ctx, `SELECT `+intentColumns+` FROM integration_intents WHERE id = ?`, id))
	if isNoRows(err) {
		return i, fault.New(fault.CodeNotFound, "integration intent %d not found", id)
	}
	return i, wrapInternal(err, "get intent")
}

// ActiveIntentForTask returns the task's open intent, if any: a promotion
// that was decided but not yet recorded as complete.
func (t *Tx) ActiveIntentForTask(id task.ID) (*Intent, error) {
	i, err := scanIntent(t.tx.QueryRowContext(t.ctx, `SELECT `+intentColumns+` FROM integration_intents WHERE task_id = ? AND status = 'intended' ORDER BY id DESC LIMIT 1`, id))
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, wrapInternal(err, "active intent")
	}
	return &i, nil
}

// IntentsForJob lists the member intents of a cohort job.
func (t *Tx) IntentsForJob(jobID int64) ([]Intent, error) {
	return t.queryIntents(`SELECT `+intentColumns+` FROM integration_intents WHERE job_id = ? ORDER BY id`, jobID)
}

// IntentsForTask lists every intent of a task, oldest first.
func (t *Tx) IntentsForTask(id task.ID) ([]Intent, error) {
	return t.queryIntents(`SELECT `+intentColumns+` FROM integration_intents WHERE task_id = ? ORDER BY id`, id)
}

// OrphanedIntents lists open intents whose owner is gone: the owning
// attempt's lease or the owning job's lease has expired. Only these are
// reconciled; a live owner is still working on its promotion.
func (t *Tx) OrphanedIntents(now time.Time) ([]Intent, error) {
	return t.queryIntents(`SELECT `+intentCols("i")+` FROM integration_intents i
		LEFT JOIN execution_attempts a ON a.id = i.attempt_id
		LEFT JOIN verification_jobs j ON j.id = i.job_id
		WHERE i.status = 'intended'
		  AND ((i.attempt_id IS NOT NULL AND a.lease_expires_at <= ?) OR (i.job_id IS NOT NULL AND j.lease_expires_at <= ?))
		ORDER BY i.id`, ms(now), ms(now))
}

func intentCols(alias string) string {
	cols := ""
	for i, c := range []string{"id", "project_id", "task_id", "attempt_id", "job_id", "run_id", "submission_id", "owner_digest", "contract_rev", "regression_digest", "target_branch", "base_revision", "source_revision", "candidate_revision", "status", "created_at", "finished_at", "summary"} {
		if i > 0 {
			cols += ", "
		}
		cols += alias + "." + c
	}
	return cols
}

func (t *Tx) queryIntents(q string, args ...any) ([]Intent, error) {
	rows, err := t.tx.QueryContext(t.ctx, q, args...)
	if err != nil {
		return nil, wrapInternal(err, "list intents")
	}
	defer rows.Close()
	var out []Intent
	for rows.Next() {
		i, err := scanIntent(rows)
		if err != nil {
			return nil, wrapInternal(err, "scan intent")
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// FinishIntent moves an open intent to completed or abandoned. It is a
// compare-and-swap on the open status, so two reconcilers cannot both
// finish the same intent.
func (t *Tx) FinishIntent(id int64, status, summary string, now time.Time) error {
	res, err := t.tx.ExecContext(t.ctx, `UPDATE integration_intents SET status = ?, summary = ?, finished_at = ? WHERE id = ? AND status = 'intended'`, status, summary, ms(now), id)
	if err != nil {
		return wrapInternal(err, "finish intent")
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fault.New(fault.CodePlanConflict, "integration intent %d is no longer open", id)
	}
	return nil
}

// SetJobStatus records a job outcome regardless of ownership; used by
// reconciliation, which acts for a verifier that is gone.
func (t *Tx) SetJobStatus(id int64, status, candidate, summary string, now time.Time) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE verification_jobs SET status = ?, candidate = ?, summary = ?, finished_at = ? WHERE id = ?`, status, candidate, summary, ms(now), id)
	return wrapInternal(err, "set job status")
}
