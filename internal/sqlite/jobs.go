package sqlite

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// JobMember is one member task of a verification job.
type JobMember struct {
	TaskID       task.ID `json:"task_id"`
	SubmissionID int64   `json:"submission_id"`
	Revision     string  `json:"revision"`
}

// Job is a durable, leased verification job (the group verifier's).
type Job struct {
	ID             int64
	ProjectID      project.ID
	Name           string // the job's stable name (stored in the legacy cohort column)
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
func (t *Tx) InsertJob(pid project.ID, name string, members []JobMember, ownerDigest string, lease time.Time, now time.Time) (int64, error) {
	body, err := json.Marshal(members)
	if err != nil {
		return 0, wrapInternal(err, "encode members")
	}
	id, err := t.insertID(`INSERT INTO verification_jobs (project_id, cohort, status, members_json, owner_digest, lease_expires_at, created_at) VALUES (?, ?, 'running', ?, ?, ?, ?)`,
		pid, name, string(body), ownerDigest, ms(lease), ms(now))
	if err != nil {
		return 0, wrapInternal(err, "insert job")
	}
	return id, nil
}

// RunningJob returns the live job with this name, if any.
func (t *Tx) RunningJob(pid project.ID, name string, now time.Time) (*Job, error) {
	row := t.tx.QueryRowContext(t.ctx, jobSelect+` WHERE project_id = ? AND cohort = ? AND status = 'running' AND lease_expires_at > ? ORDER BY id DESC LIMIT 1`, pid, name, ms(now))
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
	if err := sc.Scan(&j.ID, &j.ProjectID, &j.Name, &j.Status, &members, &j.OwnerDigest, &lease, &j.Candidate, &created, &finished, &j.Summary); err != nil {
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
		return fault.New(fault.CodeInvalidSession, "verification job %d is no longer owned by this verifier", id)
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
		return fault.New(fault.CodeInvalidSession, "verification job %d is no longer owned by this verifier", id)
	}
	return nil
}

// RecentJobs lists the newest jobs with this name, newest first.
func (t *Tx) RecentJobs(pid project.ID, name string, limit int) ([]Job, error) {
	rows, err := t.tx.QueryContext(t.ctx, jobSelect+` WHERE project_id = ? AND cohort = ? ORDER BY id DESC LIMIT ?`, pid, name, limit)
	if err != nil {
		return nil, wrapInternal(err, "list jobs")
	}
	defer discard.CloseReadOnly(rows)
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
	defer discard.CloseReadOnly(rows)
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
