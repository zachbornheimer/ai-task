package sqlite

import (
	"database/sql"
	"time"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// AttemptAuth is everything needed to authorise a token-bearing request:
// the attempt the token names and the task's current generation, read in
// the same transaction.
type AttemptAuth struct {
	Attempt    execution.Attempt
	CurrentSeq int
}

// AttemptByDigest resolves a token digest. Unknown digests are reported as
// INVALID_SESSION without revealing whether the task exists.
func (t *Tx) AttemptByDigest(digest string) (AttemptAuth, error) {
	var a AttemptAuth
	var started, expires, leaseMS int64
	var ended sql.NullInt64
	var reason sql.NullString
	err := t.tx.QueryRowContext(t.ctx, `
		SELECT a.id, a.task_id, a.seq, a.started_at, a.lease_expires_at, a.ended_at, a.end_reason, a.workspace_path, a.workspace_branch, a.lease_ms, t.current_attempt_seq
		FROM execution_attempts a JOIN tasks t ON t.id = a.task_id WHERE a.token_digest = ?`, digest).
		Scan(&a.Attempt.ID, &a.Attempt.TaskID, &a.Attempt.Seq, &started, &expires, &ended, &reason, &a.Attempt.WorkspacePath, &a.Attempt.WorkspaceBranch, &leaseMS, &a.CurrentSeq)
	if isNoRows(err) {
		return a, fault.New(fault.CodeInvalidSession, "unknown session token")
	}
	if err != nil {
		return a, wrapInternal(err, "lookup session")
	}
	a.Attempt.StartedAt = fromMS(started)
	a.Attempt.LeaseExpiresAt = fromMS(expires)
	a.Attempt.Lease = time.Duration(leaseMS) * time.Millisecond
	a.Attempt.EndedAt = nullMS(ended)
	a.Attempt.EndReason = execution.EndReason(reason.String)
	return a, nil
}

// StartAttempt atomically advances the task's generation, ends the previous
// attempt if it was still open, and records the new attempt. It returns the
// new attempt.
func (t *Tx) StartAttempt(id task.ID, prev *execution.Attempt, digest string, now time.Time, lease time.Duration) (execution.Attempt, error) {
	expires := now.Add(lease)
	seq := 1
	if prev != nil {
		seq = prev.Seq + 1
		if prev.EndedAt == nil {
			if _, err := t.tx.ExecContext(t.ctx, `UPDATE execution_attempts SET ended_at = ?, end_reason = ? WHERE id = ? AND ended_at IS NULL`, ms(now), execution.EndSuperseded, prev.ID); err != nil {
				return execution.Attempt{}, wrapInternal(err, "supersede attempt")
			}
		}
	}
	// The conditional UPDATE is the fencing write: if another transaction
	// advanced the generation first, zero rows match and we report a race.
	res, err := t.tx.ExecContext(t.ctx, `UPDATE tasks SET current_attempt_seq = ?, updated_at = ? WHERE id = ? AND current_attempt_seq = ?`, seq, ms(now), id, seq-1)
	if err != nil {
		return execution.Attempt{}, wrapInternal(err, "advance attempt generation")
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return execution.Attempt{}, fault.New(fault.CodeTaskAlreadyTaken, "task %s was taken concurrently", id)
	}
	r, err := t.tx.ExecContext(t.ctx, `INSERT INTO execution_attempts (task_id, seq, token_digest, started_at, lease_expires_at, lease_ms) VALUES (?, ?, ?, ?, ?, ?)`,
		id, seq, digest, ms(now), ms(expires), lease.Milliseconds())
	if err != nil {
		return execution.Attempt{}, wrapInternal(err, "insert attempt")
	}
	aid, _ := r.LastInsertId()
	return execution.Attempt{ID: aid, TaskID: id, Seq: seq, StartedAt: now, LeaseExpiresAt: expires, Lease: lease}, nil
}

// SetWorkspace records the attempt's worktree.
func (t *Tx) SetWorkspace(attemptID int64, path, branch string) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE execution_attempts SET workspace_path = ?, workspace_branch = ? WHERE id = ?`, path, branch, attemptID)
	return wrapInternal(err, "set workspace")
}

// PreviousWorkspaceBranch returns the branch of the newest earlier attempt
// of a task that had a workspace, so a new attempt can continue from it.
func (t *Tx) PreviousWorkspaceBranch(id task.ID) (string, error) {
	var branch string
	err := t.tx.QueryRowContext(t.ctx, `SELECT workspace_branch FROM execution_attempts WHERE task_id = ? AND workspace_branch <> '' ORDER BY seq DESC LIMIT 1`, id).Scan(&branch)
	if isNoRows(err) {
		return "", nil
	}
	return branch, wrapInternal(err, "previous workspace")
}

// RenewLease extends an open attempt's lease. The attempt must still be
// open; authority was checked by the caller in this transaction.
func (t *Tx) RenewLease(attemptID int64, expires time.Time) error {
	res, err := t.tx.ExecContext(t.ctx, `UPDATE execution_attempts SET lease_expires_at = ? WHERE id = ? AND ended_at IS NULL`, ms(expires), attemptID)
	if err != nil {
		return wrapInternal(err, "renew lease")
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fault.New(fault.CodeSessionFinished, "attempt already ended")
	}
	return nil
}

// EndAttempt closes an attempt with a reason.
func (t *Tx) EndAttempt(attemptID int64, reason execution.EndReason, now time.Time) error {
	res, err := t.tx.ExecContext(t.ctx, `UPDATE execution_attempts SET ended_at = ?, end_reason = ? WHERE id = ? AND ended_at IS NULL`, ms(now), reason, attemptID)
	if err != nil {
		return wrapInternal(err, "end attempt")
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fault.New(fault.CodeSessionFinished, "attempt already ended")
	}
	return nil
}

// Attempts lists every attempt of a task, oldest first. Token digests are
// never returned.
func (t *Tx) Attempts(id task.ID) ([]execution.Attempt, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT id, seq, started_at, lease_expires_at, ended_at, end_reason, workspace_path, workspace_branch, lease_ms FROM execution_attempts WHERE task_id = ? ORDER BY seq`, id)
	if err != nil {
		return nil, wrapInternal(err, "list attempts")
	}
	defer rows.Close()
	var out []execution.Attempt
	for rows.Next() {
		var a execution.Attempt
		var started, expires, leaseMS int64
		var ended sql.NullInt64
		var reason sql.NullString
		if err := rows.Scan(&a.ID, &a.Seq, &started, &expires, &ended, &reason, &a.WorkspacePath, &a.WorkspaceBranch, &leaseMS); err != nil {
			return nil, wrapInternal(err, "scan attempt")
		}
		a.TaskID = id
		a.StartedAt, a.LeaseExpiresAt = fromMS(started), fromMS(expires)
		a.Lease = time.Duration(leaseMS) * time.Millisecond
		a.EndedAt = nullMS(ended)
		a.EndReason = execution.EndReason(reason.String)
		out = append(out, a)
	}
	return out, rows.Err()
}
