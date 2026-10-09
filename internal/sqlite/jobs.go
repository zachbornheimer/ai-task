package sqlite

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// JobMember is one cohort member's submission in a verification job.
type JobMember struct {
	TaskID       task.ID `json:"task_id"`
	SubmissionID int64   `json:"submission_id"`
	Revision     string  `json:"revision"`
}

// Job is a durable cohort verification job.
type Job struct {
	ID             int64
	ProjectID      project.ID
	Cohort         string
	Status         string // running | passed | failed | error (environment) | interrupted (verifier died)
	Members        []JobMember
	OwnerDigest    string
	LeaseExpiresAt time.Time
	Candidate      string
	CreatedAt      time.Time
	FinishedAt     *time.Time
	Summary        string
}

// InsertJob records a running job owned by the caller.
func (t *Tx) InsertJob(pid project.ID, cohort string, members []JobMember, ownerDigest string, lease time.Time, now time.Time) (int64, error) {
	body, err := json.Marshal(members)
	if err != nil {
		return 0, wrapInternal(err, "encode members")
	}
	res, err := t.tx.ExecContext(t.ctx, `INSERT INTO verification_jobs (project_id, cohort, status, members_json, owner_digest, lease_expires_at, created_at) VALUES (?, ?, 'running', ?, ?, ?, ?)`,
		pid, cohort, string(body), ownerDigest, ms(lease), ms(now))
	if err != nil {
		return 0, wrapInternal(err, "insert job")
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// RunningJob returns the live job for a cohort, if any.
func (t *Tx) RunningJob(pid project.ID, cohort string, now time.Time) (*Job, error) {
	row := t.tx.QueryRowContext(t.ctx, jobSelect+` WHERE project_id = ? AND cohort = ? AND status = 'running' AND lease_expires_at > ? ORDER BY id DESC LIMIT 1`, pid, cohort, ms(now))
	j, err := scanJob(row)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, wrapInternal(err, "running job")
	}
	return &j, nil
}

const jobSelect = `SELECT id, project_id, cohort, status, members_json, owner_digest, lease_expires_at, candidate, created_at, finished_at, summary FROM verification_jobs`

func scanJob(sc interface{ Scan(...any) error }) (Job, error) {
	var j Job
	var members string
	var lease, created int64
	var finished sql.NullInt64
	if err := sc.Scan(&j.ID, &j.ProjectID, &j.Cohort, &j.Status, &members, &j.OwnerDigest, &lease, &j.Candidate, &created, &finished, &j.Summary); err != nil {
		return j, err
	}
	if err := json.Unmarshal([]byte(members), &j.Members); err != nil {
		return j, wrapInternal(err, "decode members")
	}
	j.LeaseExpiresAt, j.CreatedAt = fromMS(lease), fromMS(created)
	j.FinishedAt = nullMS(finished)
	return j, nil
}

// GetJob loads a job.
func (t *Tx) GetJob(id int64) (Job, error) {
	j, err := scanJob(t.tx.QueryRowContext(t.ctx, jobSelect+` WHERE id = ?`, id))
	if isNoRows(err) {
		return j, fault.New(fault.CodeNotFound, "verification job %d not found", id)
	}
	return j, wrapInternal(err, "get job")
}

// RenewJob extends a job lease; the owner must match.
func (t *Tx) RenewJob(id int64, ownerDigest string, lease time.Time) error {
	res, err := t.tx.ExecContext(t.ctx, `UPDATE verification_jobs SET lease_expires_at = ? WHERE id = ? AND owner_digest = ? AND status = 'running'`, ms(lease), id, ownerDigest)
	if err != nil {
		return wrapInternal(err, "renew job")
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fault.New(fault.CodeSessionSuperseded, "verification job %d is no longer owned by this verifier", id)
	}
	return nil
}

// FinishJob records a job's outcome; the owner must still hold it.
func (t *Tx) FinishJob(id int64, ownerDigest, status, candidate, summary string, now time.Time) error {
	res, err := t.tx.ExecContext(t.ctx, `UPDATE verification_jobs SET status = ?, candidate = ?, summary = ?, finished_at = ? WHERE id = ? AND owner_digest = ? AND status = 'running'`, status, candidate, summary, ms(now), id, ownerDigest)
	if err != nil {
		return wrapInternal(err, "finish job")
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fault.New(fault.CodeSessionSuperseded, "verification job %d is no longer owned by this verifier", id)
	}
	return nil
}

// RecentJobs lists a cohort's newest jobs, newest first.
func (t *Tx) RecentJobs(pid project.ID, cohort string, limit int) ([]Job, error) {
	rows, err := t.tx.QueryContext(t.ctx, jobSelect+` WHERE project_id = ? AND cohort = ? ORDER BY id DESC LIMIT ?`, pid, cohort, limit)
	if err != nil {
		return nil, wrapInternal(err, "list cohort jobs")
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, wrapInternal(err, "scan job")
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// JobsForProject lists jobs, newest first.
func (t *Tx) JobsForProject(pid project.ID, limit int) ([]Job, error) {
	rows, err := t.tx.QueryContext(t.ctx, jobSelect+` WHERE project_id = ? ORDER BY id DESC LIMIT ?`, pid, limit)
	if err != nil {
		return nil, wrapInternal(err, "list jobs")
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, wrapInternal(err, "scan job")
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// InsertIntegration records a promotion.
func (t *Tx) InsertIntegration(pid project.ID, taskID task.ID, jobID int64, target, base, source, result string, now time.Time) error {
	var tid, jid any
	if taskID != "" {
		tid = taskID
	}
	if jobID != 0 {
		jid = jobID
	}
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO integration_records (project_id, task_id, job_id, target_branch, base_revision, source_revision, result_revision, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, tid, jid, target, base, source, result, ms(now))
	return wrapInternal(err, "insert integration record")
}

// Integration is a stored promotion record.
type Integration struct {
	ID             int64     `json:"id"`
	TaskID         task.ID   `json:"task_id,omitempty"`
	JobID          int64     `json:"job_id,omitempty"`
	TargetBranch   string    `json:"target_branch"`
	BaseRevision   string    `json:"base_revision"`
	SourceRevision string    `json:"source_revision"`
	ResultRevision string    `json:"result_revision"`
	CreatedAt      time.Time `json:"created_at"`
}

// IntegrationsForTask lists promotions that included a task.
func (t *Tx) IntegrationsForTask(id task.ID) ([]Integration, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT id, task_id, job_id, target_branch, base_revision, source_revision, result_revision, created_at FROM integration_records WHERE task_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, wrapInternal(err, "list integrations")
	}
	defer rows.Close()
	var out []Integration
	for rows.Next() {
		var i Integration
		var tid sql.NullString
		var jid sql.NullInt64
		var created int64
		if err := rows.Scan(&i.ID, &tid, &jid, &i.TargetBranch, &i.BaseRevision, &i.SourceRevision, &i.ResultRevision, &created); err != nil {
			return nil, wrapInternal(err, "scan integration")
		}
		i.TaskID, i.JobID, i.CreatedAt = task.ID(tid.String), jid.Int64, fromMS(created)
		out = append(out, i)
	}
	return out, rows.Err()
}
