package sqlite

import (
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// InsertSubmission records an immutable submission for an attempt and
// points the task at it. The newest submission is the one that must be
// judged, so any completion fact for an older submission is withdrawn.
func (t *Tx) InsertSubmission(id task.ID, attemptID int64, revision, cohort string, now time.Time) (int64, error) {
	res, err := t.tx.ExecContext(t.ctx, `INSERT INTO submissions (task_id, attempt_id, revision, cohort, submitted_at) VALUES (?, ?, ?, ?, ?)`,
		id, attemptID, revision, cohort, ms(now))
	if err != nil {
		return 0, wrapInternal(err, "insert submission")
	}
	sid, _ := res.LastInsertId()
	if _, err := t.tx.ExecContext(t.ctx, `UPDATE tasks SET latest_submission_id = ?, completed_at = NULL, completed_submission_id = NULL, updated_at = ? WHERE id = ?`, sid, ms(now), id); err != nil {
		return 0, wrapInternal(err, "point task at submission")
	}
	return sid, nil
}

// NewRun describes a run to insert.
type NewRun struct {
	TaskID       task.ID
	AttemptID    int64
	SubmissionID int64
	JobID        int64
	Mode         verification.Mode
	Status       RunStatus
	Revision     string
	Policy       verification.Policy
	Environment  string
	Summary      string
}

// InsertRun records a verification run.
func (t *Tx) InsertRun(r NewRun, now time.Time) (int64, error) {
	body, err := r.Policy.Canonical()
	if err != nil {
		return 0, wrapInternal(err, "encode run policy")
	}
	var started, finished, attempt, submission, job any
	switch {
	case r.Status.Final():
		started, finished = ms(now), ms(now)
	case r.Status == RunRunning:
		started = ms(now)
	}
	if r.AttemptID != 0 {
		attempt = r.AttemptID
	}
	if r.SubmissionID != 0 {
		submission = r.SubmissionID
	}
	if r.JobID != 0 {
		job = r.JobID
	}
	res, err := t.tx.ExecContext(t.ctx, `INSERT INTO verification_runs (task_id, attempt_id, submission_id, job_id, mode, status, revision, policy_json, policy_digest, environment, created_at, started_at, finished_at, summary)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TaskID, attempt, submission, job, r.Mode, r.Status, r.Revision, string(body), r.Policy.Digest(), r.Environment, ms(now), started, finished, r.Summary)
	if err != nil {
		return 0, wrapInternal(err, "insert verification run")
	}
	rid, _ := res.LastInsertId()
	return rid, nil
}

// FinishRun records the final state of a run.
func (t *Tx) FinishRun(runID int64, status RunStatus, summary string, now time.Time) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE verification_runs SET status = ?, summary = ?, finished_at = ? WHERE id = ?`, status, summary, ms(now), runID)
	return wrapInternal(err, "finish verification run")
}

// SetRunIntegratedRevision records the revision a run's evidence finally
// applied to after integration.
func (t *Tx) SetRunIntegratedRevision(runID int64, rev string) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE verification_runs SET integrated_revision = ? WHERE id = ?`, rev, runID)
	return wrapInternal(err, "set integrated revision")
}

const runColumns = `id, task_id, attempt_id, submission_id, job_id, mode, status, revision, integrated_revision, policy_json, policy_digest, environment, created_at, started_at, finished_at, summary`

func scanRun(sc interface{ Scan(...any) error }) (Run, error) {
	var rs runScan
	var taskID task.ID
	var submission nullInt
	err := sc.Scan(&rs.id, &taskID, &rs.attempt, &submission, &rs.job, &rs.mode, &rs.status, &rs.revision, &rs.integrated, &rs.policy, &rs.digest, &rs.env, &rs.created, &rs.started, &rs.finished, &rs.summary)
	if err != nil {
		return Run{}, err
	}
	r, err := rs.toRun(taskID, submission.Int64)
	if err != nil {
		return Run{}, err
	}
	return *r, nil
}

// GetRun loads one run.
func (t *Tx) GetRun(runID int64) (Run, error) {
	row := t.tx.QueryRowContext(t.ctx, `SELECT `+runColumns+` FROM verification_runs WHERE id = ?`, runID)
	r, err := scanRun(row)
	if isNoRows(err) {
		return Run{}, fault.New(fault.CodeNotFound, "verification run %d not found", runID)
	}
	return r, wrapInternal(err, "get run")
}

// LatestRunID returns the newest run of a submission (0 if none).
func (t *Tx) LatestRunID(submissionID int64) (int64, error) {
	var id int64
	err := t.tx.QueryRowContext(t.ctx, `SELECT id FROM verification_runs WHERE submission_id = ? ORDER BY id DESC LIMIT 1`, submissionID).Scan(&id)
	if isNoRows(err) {
		return 0, nil
	}
	return id, wrapInternal(err, "latest run")
}

// Submissions lists every submission of a task, oldest first, each with its
// newest run.
func (t *Tx) Submissions(id task.ID) ([]Submission, error) {
	rows, err := t.tx.QueryContext(t.ctx, `
		SELECT s.id, s.attempt_id, a.seq, s.revision, s.cohort, s.submitted_at,
		       vr.id, vr.attempt_id, vr.job_id, vr.mode, vr.status, vr.revision, vr.integrated_revision, vr.policy_json, vr.policy_digest, vr.environment, vr.created_at, vr.started_at, vr.finished_at, vr.summary
		FROM submissions s JOIN execution_attempts a ON a.id = s.attempt_id
		LEFT JOIN verification_runs vr ON vr.id = (SELECT id FROM verification_runs WHERE submission_id = s.id ORDER BY id DESC LIMIT 1)
		WHERE s.task_id = ? ORDER BY s.id`, id)
	if err != nil {
		return nil, wrapInternal(err, "list submissions")
	}
	defer rows.Close()
	var out []Submission
	for rows.Next() {
		var s Submission
		var at int64
		var rs runScan
		if err := rows.Scan(&s.ID, &s.AttemptID, &s.AttemptSeq, &s.Revision, &s.Cohort, &at, &rs.id, &rs.attempt, &rs.job, &rs.mode, &rs.status, &rs.revision, &rs.integrated, &rs.policy, &rs.digest, &rs.env, &rs.created, &rs.started, &rs.finished, &rs.summary); err != nil {
			return nil, wrapInternal(err, "scan submission")
		}
		s.TaskID = id
		s.SubmittedAt = fromMS(at)
		if s.Run, err = rs.toRun(id, s.ID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RunsForTask lists every run of a task (diagnostic and final), oldest
// first.
func (t *Tx) RunsForTask(id task.ID) ([]Run, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT `+runColumns+` FROM verification_runs WHERE task_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, wrapInternal(err, "list runs")
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, wrapInternal(err, "scan run")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RunsForJob lists the member runs of a cohort job.
func (t *Tx) RunsForJob(jobID int64) ([]Run, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT `+runColumns+` FROM verification_runs WHERE job_id = ? ORDER BY id`, jobID)
	if err != nil {
		return nil, wrapInternal(err, "list job runs")
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, wrapInternal(err, "scan run")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertEvidence records one check result.
func (t *Tx) InsertEvidence(e verification.Evidence) (int64, error) {
	var reusedFrom any
	if e.ReusedFrom != 0 {
		reusedFrom = e.ReusedFrom
	}
	res, err := t.tx.ExecContext(t.ctx, `INSERT INTO verification_results
		(run_id, check_id, check_version, check_digest, required, revision, policy_digest, started_at, finished_at, exit_code, outcome, reused, reused_from, message, stdout, stderr)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.RunID, e.CheckID, e.CheckVersion, e.CheckDigest, e.Required, e.Revision, e.PolicyDigest, ms(e.StartedAt), ms(e.FinishedAt), e.ExitCode, e.Outcome, e.Reused, reusedFrom, e.Message, e.Stdout, e.Stderr)
	if err != nil {
		return 0, wrapInternal(err, "insert evidence")
	}
	id, _ := res.LastInsertId()
	return id, nil
}

const evidenceColumns = `id, run_id, check_id, check_version, check_digest, required, revision, policy_digest, started_at, finished_at, exit_code, outcome, reused, reused_from, message, stdout, stderr`

func scanEvidence(sc interface{ Scan(...any) error }) (verification.Evidence, error) {
	var e verification.Evidence
	var started, finished int64
	var reusedFrom nullInt
	err := sc.Scan(&e.ID, &e.RunID, &e.CheckID, &e.CheckVersion, &e.CheckDigest, &e.Required, &e.Revision, &e.PolicyDigest, &started, &finished, &e.ExitCode, &e.Outcome, &e.Reused, &reusedFrom, &e.Message, &e.Stdout, &e.Stderr)
	if err != nil {
		return e, err
	}
	e.StartedAt, e.FinishedAt = fromMS(started), fromMS(finished)
	e.ReusedFrom = reusedFrom.Int64
	return e, nil
}

// EvidenceForRun lists a run's evidence in execution order.
func (t *Tx) EvidenceForRun(runID int64) ([]verification.Evidence, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT `+evidenceColumns+` FROM verification_results WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, wrapInternal(err, "list evidence")
	}
	defer rows.Close()
	var out []verification.Evidence
	for rows.Next() {
		e, err := scanEvidence(rows)
		if err != nil {
			return nil, wrapInternal(err, "scan evidence")
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ReconcileRuns closes final runs whose owning attempt's lease has expired
// (the verifying process is gone) and resets cohort member runs whose job
// lease expired to pending so the job can be retried. Returns how many
// rows changed.
func (t *Tx) ReconcileRuns(now time.Time) (int64, error) {
	res, err := t.tx.ExecContext(t.ctx, `UPDATE verification_runs SET status = 'error', finished_at = ?, summary = 'interrupted: the verifying attempt lease expired before the run finished'
		WHERE status = 'running' AND mode = 'complete' AND attempt_id IN (SELECT id FROM execution_attempts WHERE lease_expires_at <= ?)`, ms(now), ms(now))
	if err != nil {
		return 0, wrapInternal(err, "reconcile final runs")
	}
	n, _ := res.RowsAffected()
	// Diagnostic runs whose attempt ended or expired while they were
	// recorded running: the process is gone; close them (no task failure).
	resD, err := t.tx.ExecContext(t.ctx, `UPDATE verification_runs SET status = 'error', finished_at = ?, summary = 'interrupted: the attempt ended or its lease expired before the diagnostic run finished'
		WHERE status = 'running' AND mode IN ('task', 'regression') AND attempt_id IN (SELECT id FROM execution_attempts WHERE ended_at IS NOT NULL OR lease_expires_at <= ?)`, ms(now), ms(now))
	if err != nil {
		return 0, wrapInternal(err, "reconcile diagnostic runs")
	}
	nD, _ := resD.RowsAffected()
	n += nD
	res2, err := t.tx.ExecContext(t.ctx, `UPDATE verification_runs SET status = 'pending', summary = 'verifier interrupted; waiting for a new verifier'
		WHERE status = 'running' AND mode = 'cohort' AND job_id IN (SELECT id FROM verification_jobs WHERE status = 'running' AND lease_expires_at <= ?)`, ms(now))
	if err != nil {
		return 0, wrapInternal(err, "reconcile cohort runs")
	}
	n2, _ := res2.RowsAffected()
	// 'interrupted' (not 'error'): a dead verifier is not a failed
	// promotion, so it does not count toward the cohort's retry backoff.
	res3, err := t.tx.ExecContext(t.ctx, `UPDATE verification_jobs SET status = 'interrupted', finished_at = ?, summary = 'verifier lease expired; job will be retried'
		WHERE status = 'running' AND lease_expires_at <= ?`, ms(now), ms(now))
	if err != nil {
		return 0, wrapInternal(err, "reconcile jobs")
	}
	n3, _ := res3.RowsAffected()
	return n + n2 + n3, nil
}
