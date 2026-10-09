package sqlite

import (
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// InsertSubmission records a submission for an attempt and points the task
// at it. The newest submission is the one that must be judged, so any
// completion fact recorded for an older submission is withdrawn here.
// It returns the submission ID.
func (t *Tx) InsertSubmission(id task.ID, attemptID int64, revision string, now time.Time) (int64, error) {
	res, err := t.tx.ExecContext(t.ctx, `INSERT INTO submissions (task_id, attempt_id, revision, submitted_at) VALUES (?, ?, ?, ?)`,
		id, attemptID, revision, ms(now))
	if err != nil {
		return 0, wrapInternal(err, "insert submission")
	}
	sid, _ := res.LastInsertId()
	if _, err := t.tx.ExecContext(t.ctx, `UPDATE tasks SET latest_submission_id = ?, completed_at = NULL, completed_submission_id = NULL, updated_at = ? WHERE id = ?`, sid, ms(now), id); err != nil {
		return 0, wrapInternal(err, "point task at submission")
	}
	return sid, nil
}

// InsertRun records a verification run under an effective policy.
func (t *Tx) InsertRun(submissionID int64, status RunStatus, policy verification.Policy, environment, summary string, now time.Time) (int64, error) {
	body, err := policy.Canonical()
	if err != nil {
		return 0, wrapInternal(err, "encode run policy")
	}
	var started, finished any
	switch {
	case status.Final():
		started, finished = ms(now), ms(now)
	case status == RunRunning:
		started = ms(now)
	}
	res, err := t.tx.ExecContext(t.ctx, `INSERT INTO verification_runs (submission_id, status, policy_json, policy_digest, environment, created_at, started_at, finished_at, summary)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		submissionID, status, string(body), policy.Digest(), environment, ms(now), started, finished, summary)
	if err != nil {
		return 0, wrapInternal(err, "insert verification run")
	}
	rid, _ := res.LastInsertId()
	return rid, nil
}

// StartRun marks a pending run as running.
func (t *Tx) StartRun(runID int64, environment string, now time.Time) error {
	res, err := t.tx.ExecContext(t.ctx, `UPDATE verification_runs SET status = ?, environment = ?, started_at = ? WHERE id = ? AND status = ?`, RunRunning, environment, ms(now), runID, RunPending)
	if err != nil {
		return wrapInternal(err, "start verification run")
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fault.New(fault.CodeVerificationRunning, "run %d is not pending", runID)
	}
	return nil
}

// FinishRun records the final state of a run.
func (t *Tx) FinishRun(runID int64, status RunStatus, summary string, now time.Time) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE verification_runs SET status = ?, summary = ?, finished_at = ? WHERE id = ?`, status, summary, ms(now), runID)
	return wrapInternal(err, "finish verification run")
}

// GetRun loads one run.
func (t *Tx) GetRun(runID int64) (Run, error) {
	var rs runScan
	var sub int64
	err := t.tx.QueryRowContext(t.ctx, `SELECT id, submission_id, status, policy_json, policy_digest, environment, created_at, started_at, finished_at, summary FROM verification_runs WHERE id = ?`, runID).
		Scan(&rs.id, &sub, &rs.status, &rs.policy, &rs.digest, &rs.env, &rs.created, &rs.started, &rs.finished, &rs.summary)
	if isNoRows(err) {
		return Run{}, fault.New(fault.CodeNotFound, "verification run %d not found", runID)
	}
	if err != nil {
		return Run{}, wrapInternal(err, "get run")
	}
	r, err := rs.toRun(sub)
	if err != nil {
		return Run{}, err
	}
	return *r, nil
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
		SELECT s.id, s.attempt_id, a.seq, s.revision, s.submitted_at,
		       vr.id, vr.status, vr.policy_json, vr.policy_digest, vr.environment, vr.created_at, vr.started_at, vr.finished_at, vr.summary
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
		if err := rows.Scan(&s.ID, &s.AttemptID, &s.AttemptSeq, &s.Revision, &at, &rs.id, &rs.status, &rs.policy, &rs.digest, &rs.env, &rs.created, &rs.started, &rs.finished, &rs.summary); err != nil {
			return nil, wrapInternal(err, "scan submission")
		}
		s.TaskID = id
		s.SubmittedAt = fromMS(at)
		if s.Run, err = rs.toRun(s.ID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Runs lists every run of a submission, oldest first.
func (t *Tx) Runs(submissionID int64) ([]Run, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT id, submission_id, status, policy_json, policy_digest, environment, created_at, started_at, finished_at, summary FROM verification_runs WHERE submission_id = ? ORDER BY id`, submissionID)
	if err != nil {
		return nil, wrapInternal(err, "list runs")
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var rs runScan
		var sub int64
		if err := rows.Scan(&rs.id, &sub, &rs.status, &rs.policy, &rs.digest, &rs.env, &rs.created, &rs.started, &rs.finished, &rs.summary); err != nil {
			return nil, wrapInternal(err, "scan run")
		}
		r, err := rs.toRun(sub)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
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

// FindReusableEvidence returns the newest original passed evidence for the
// same check content at the same revision, if any. The caller applies
// verification.Reusable to the result as the authoritative rule.
func (t *Tx) FindReusableEvidence(checkDigest, revision string) (*verification.Evidence, error) {
	if revision == "" {
		return nil, nil
	}
	row := t.tx.QueryRowContext(t.ctx, `SELECT `+evidenceColumns+` FROM verification_results WHERE check_digest = ? AND revision = ? AND outcome = ? AND reused = 0 ORDER BY id DESC LIMIT 1`,
		checkDigest, revision, verification.OutcomePassed)
	e, err := scanEvidence(row)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, wrapInternal(err, "find reusable evidence")
	}
	return &e, nil
}
