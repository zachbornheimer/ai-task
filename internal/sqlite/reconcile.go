package sqlite

import (
	"fmt"
	"time"

	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/process"
)

// Each predicate names one way a run or job is left dead, and carries one
// placeholder: the current time. ReconcilePending asks whether any matches;
// ReconcileRuns closes what does. They share the text so the question and
// the cure cannot drift apart.
const (
	// runLeaseExpired: a final run whose attempt's lease ran out.
	runLeaseExpired = `status = 'running' AND mode = 'complete' AND attempt_id IN (SELECT id FROM execution_attempts WHERE lease_expires_at <= ?)`
	// runHeartbeatStale: a run whose process stopped stamping its
	// heartbeat for two intervals, even while its attempt lease is live.
	runHeartbeatStale = `status = 'running' AND mode IN ('complete', 'proof', 'regression') AND heartbeat_every_ms > 0 AND heartbeat_at + 2 * heartbeat_every_ms <= ?`
	// diagnosticAttemptGone: a diagnostic run whose attempt ended or expired.
	diagnosticAttemptGone = `status = 'running' AND mode IN ('proof', 'regression') AND attempt_id IN (SELECT id FROM execution_attempts WHERE ended_at IS NOT NULL OR lease_expires_at <= ?)`
	// epicVerifierDead: an epic (group-mode) run whose job's lease ran out.
	epicVerifierDead = `status = 'running' AND mode = 'group' AND job_id IN (SELECT id FROM verification_jobs WHERE status = 'running' AND lease_expires_at <= ?)`
	// jobLeaseExpired: a verification job whose verifier's lease ran out.
	jobLeaseExpired = `status = 'running' AND lease_expires_at <= ?`
)

// ReconcilePending reports whether ReconcileRuns would change anything: one
// read, plus a process probe for the runs that record an owner. Callers
// that only read ask first, so a board with nothing to reconcile costs a
// read instead of a write transaction.
func (t *Tx) ReconcilePending(now time.Time) (bool, error) {
	at := ms(now)
	var pending int
	if err := t.tx.QueryRowContext(t.ctx, `SELECT CASE WHEN
		   EXISTS (SELECT 1 FROM verification_runs WHERE `+runLeaseExpired+`)
		 OR EXISTS (SELECT 1 FROM verification_runs WHERE `+runHeartbeatStale+`)
		 OR EXISTS (SELECT 1 FROM verification_runs WHERE `+diagnosticAttemptGone+`)
		 OR EXISTS (SELECT 1 FROM verification_runs WHERE `+epicVerifierDead+`)
		 OR EXISTS (SELECT 1 FROM verification_jobs WHERE `+jobLeaseExpired+`)
		THEN 1 ELSE 0 END`, at, at, at, at, at).Scan(&pending); err != nil {
		return false, wrapInternal(err, "probe for dead runs")
	}
	if pending == 1 {
		return true, nil
	}
	gone, err := t.runsOfGoneOwners()
	return len(gone) > 0, err
}

// ReconcileRuns closes final runs whose owning attempt's lease has expired
// (the verifying process is gone), and closes group runs whose job lease
// expired. Returns how many rows changed.
func (t *Tx) ReconcileRuns(now time.Time) (int64, error) {
	n, err := t.endRuns("reconcile final runs", runLeaseExpired, "interrupted: the verifying attempt lease expired before the run finished", now)
	if err != nil {
		return 0, err
	}
	// A run whose owning process no longer exists is dead now, whatever its
	// heartbeat and attempt lease still say.
	nO, err := t.abandonRunsOfGoneOwners(now)
	if err != nil {
		return 0, err
	}
	n += nO
	// A run whose process stopped stamping its heartbeat is dead even while
	// its attempt lease is still live (and even when its owner cannot be
	// probed).
	nH, err := t.endRuns("reap lost runs", runHeartbeatStale, abandonedNoHeartbeat, now)
	if err != nil {
		return 0, err
	}
	n += nH
	// Diagnostic runs whose attempt ended or expired while they were
	// recorded running: the process is gone; close them (no task failure).
	nD, err := t.endRuns("reconcile diagnostic runs", diagnosticAttemptGone, "interrupted: the attempt ended or its lease expired before the diagnostic run finished", now)
	if err != nil {
		return 0, err
	}
	n += nD
	// An epic run whose verifier died is closed as an error, so the epic
	// reads as pending again and the next worker reruns it; it must run
	// before the jobs below are marked interrupted.
	nE, err := t.endRuns("reconcile epic runs", epicVerifierDead, "interrupted: the epic verifier died before the run finished; the epic will be verified again", now)
	if err != nil {
		return 0, err
	}
	n += nE
	// 'interrupted' (not 'error'): a dead verifier is not a failed
	// promotion, so it does not count toward the job's retry backoff.
	res, err := t.tx.ExecContext(t.ctx, `UPDATE verification_jobs SET status = 'interrupted', finished_at = ?, summary = 'verifier lease expired; job will be retried'
		WHERE `+jobLeaseExpired, ms(now), ms(now))
	if err != nil {
		return 0, wrapInternal(err, "reconcile jobs")
	}
	nJ, _ := res.RowsAffected()
	return n + nJ, nil
}

// endRuns closes the running runs that match where (one placeholder: now)
// as errors carrying summary, returning how many it closed.
func (t *Tx) endRuns(what, where, summary string, now time.Time) (int64, error) {
	res, err := t.tx.ExecContext(t.ctx, `UPDATE verification_runs SET status = 'error', finished_at = ?, summary = ? WHERE `+where, ms(now), summary, ms(now))
	if err != nil {
		return 0, wrapInternal(err, what)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ownedRun is a running run and the process that executes it.
type ownedRun struct {
	id    int64
	owner process.Owner
}

// runsOfGoneOwners lists running runs whose recorded owner process
// provably no longer exists.
func (t *Tx) runsOfGoneOwners() ([]ownedRun, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT id, owner_pid, owner_host, owner_start FROM verification_runs
		WHERE status = 'running' AND mode IN ('complete', 'proof', 'regression') AND owner_pid > 0`)
	if err != nil {
		return nil, wrapInternal(err, "list running runs")
	}
	defer discard.CloseReadOnly(rows)
	var gone []ownedRun
	for rows.Next() {
		var o ownedRun
		if err := rows.Scan(&o.id, &o.owner.PID, &o.owner.Host, &o.owner.Start); err != nil {
			return nil, wrapInternal(err, "scan running run")
		}
		if o.owner.Gone() {
			gone = append(gone, o)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, wrapInternal(err, "list running runs")
	}
	return gone, nil
}

// abandonRunsOfGoneOwners closes running runs whose recorded owner process
// provably no longer exists. It returns how many it closed.
func (t *Tx) abandonRunsOfGoneOwners(now time.Time) (int64, error) {
	gone, err := t.runsOfGoneOwners()
	if err != nil {
		return 0, err
	}
	var n int64
	for _, o := range gone {
		res, err := t.tx.ExecContext(t.ctx, `UPDATE verification_runs SET status = 'error', finished_at = ?, summary = ? WHERE id = ? AND status = 'running'`,
			ms(now), fmt.Sprintf(abandonedOwnerGone, o.owner.PID), o.id)
		if err != nil {
			return 0, wrapInternal(err, "abandon run of a gone owner")
		}
		c, _ := res.RowsAffected()
		n += c
	}
	return n, nil
}
