package app

import (
	"context"
	"time"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// TakeRequest selects work. Task picks a specific task; when nil, Project
// must be set and the engine chooses (interrupted work first, then failed
// verification, then fresh work, oldest first).
type TakeRequest struct {
	Project project.ID
	Task    *task.ID
	Lease   time.Duration // zero = execution.DefaultLease
}

// Take atomically starts a new execution attempt and returns its session.
// Everything happens in one immediate transaction: eligibility is checked
// against the facts as of the claim, the generation is advanced, the attempt
// is inserted, and the handoff is read from the same snapshot.
func (e *Engine) Take(ctx context.Context, req TakeRequest) (Session, error) {
	lease, err := execution.LeaseDuration(req.Lease)
	if err != nil {
		return Session{}, err
	}
	if req.Task == nil && req.Project == "" {
		return Session{}, fault.New(fault.CodeNoProject, "automatic take needs a project")
	}
	now := e.now()
	token := execution.NewToken()
	var sess Session
	err = e.store.Write(ctx, func(tx *sqlite.Tx) error {
		var r sqlite.Record
		if req.Task != nil {
			var err error
			if r, err = tx.GetRecord(*req.Task); err != nil {
				return err
			}
			if err := notTakeable(r, now); err != nil {
				return err
			}
		} else {
			if _, err := tx.GetProject(req.Project); err != nil {
				return err
			}
			cand, ok, err := tx.TakeCandidate(req.Project, now)
			if err != nil {
				return err
			}
			if !ok {
				return fault.New(fault.CodeNoAvailableTask, "no takeable task in project %s", req.Project)
			}
			r = cand
			// The SQL pre-filter must agree with the domain rule; if it does
			// not, that is a bug, never a silent wrong claim.
			if err := notTakeable(r, now); err != nil {
				return fault.Wrap(err, fault.CodeInternal, "candidate selection disagreed with status rules")
			}
		}
		attempt, err := tx.StartAttempt(r.Task.ID, r.Attempt, token.Digest(), now, now.Add(lease))
		if err != nil {
			return err
		}
		// Re-read so the view reflects the new attempt.
		r2, err := tx.GetRecord(r.Task.ID)
		if err != nil {
			return err
		}
		view, err := e.buildView(tx, r2, now)
		if err != nil {
			return err
		}
		handoff := *view.Handoff
		view.Handoff = nil // emitted once, at the top level of the session
		sess = Session{
			TaskID: r.Task.ID, Token: token, AttemptSeq: attempt.Seq, ExpiresAt: attempt.LeaseExpiresAt,
			Resumed: attempt.Seq > 1, Task: view, Handoff: handoff,
		}
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	return sess, nil
}

// notTakeable maps a non-takeable status to the error an agent should see.
func notTakeable(r sqlite.Record, now time.Time) error {
	st := r.Status(now)
	if st.Takeable() {
		return nil
	}
	id := r.Task.ID
	switch st {
	case task.StatusBlocked:
		return fault.New(fault.CodeTaskBlocked, "task %s is blocked by %d incomplete prerequisite(s); run `tasks deps list %s`", id, r.UnmetDependencies, id)
	case task.StatusInProgress:
		return fault.New(fault.CodeTaskAlreadyTaken, "task %s is held by attempt %d until %s", id, r.Attempt.Seq, r.Attempt.LeaseExpiresAt.Format(time.RFC3339))
	case task.StatusAwaitingVerification:
		return fault.New(fault.CodeTaskAwaitingVerification, "task %s has a submission awaiting verification", id)
	case task.StatusAwaitingIntegration:
		return fault.New(fault.CodeTaskAwaitingIntegration, "task %s is verified and awaiting integration", id)
	case task.StatusComplete:
		return fault.New(fault.CodeTaskComplete, "task %s is complete", id)
	}
	return fault.New(fault.CodeInternal, "task %s is in unexpected status %s", id, st)
}

// authorize resolves a token to an attempt with live authority. It must be
// called inside the transaction that performs the mutation.
func (e *Engine) authorize(tx *sqlite.Tx, token execution.Token, now time.Time) (execution.Attempt, error) {
	auth, err := tx.AttemptByDigest(token.Digest())
	if err != nil {
		return execution.Attempt{}, err
	}
	if err := execution.Authorize(auth.Attempt, auth.CurrentSeq, now); err != nil {
		return execution.Attempt{}, err
	}
	return auth.Attempt, nil
}

// Log appends an entry under a session's authority. Validation of the token
// and the insert commit together; an acknowledged log is durable.
func (e *Engine) Log(ctx context.Context, token execution.Token, entry execution.LogEntry) (execution.RecordedLog, error) {
	if err := entry.Validate(); err != nil {
		return execution.RecordedLog{}, err
	}
	now := e.now()
	var rec execution.RecordedLog
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		a, err := e.authorize(tx, token, now)
		if err != nil {
			return err
		}
		seq, err := tx.InsertLog(a.TaskID, a.ID, entry, now)
		if err != nil {
			return err
		}
		rec = execution.RecordedLog{Seq: seq, AttemptSeq: a.Seq, At: now, LogEntry: entry}
		return nil
	})
	return rec, err
}

// Renew extends the lease of a live session and returns the new expiry.
func (e *Engine) Renew(ctx context.Context, token execution.Token, lease time.Duration) (time.Time, error) {
	d, err := execution.LeaseDuration(lease)
	if err != nil {
		return time.Time{}, err
	}
	now := e.now()
	expires := now.Add(d)
	err = e.store.Write(ctx, func(tx *sqlite.Tx) error {
		a, err := e.authorize(tx, token, now)
		if err != nil {
			return err
		}
		return tx.RenewLease(a.ID, expires)
	})
	return expires, err
}

// Whoami resolves a token to its task without mutating anything, so an agent
// that lost context can learn which task its token belongs to.
func (e *Engine) Whoami(ctx context.Context, token execution.Token) (TaskView, error) {
	now := e.now()
	var v TaskView
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		auth, err := tx.AttemptByDigest(token.Digest())
		if err != nil {
			return err
		}
		r, err := tx.GetRecord(auth.Attempt.TaskID)
		if err != nil {
			return err
		}
		v, err = e.buildView(tx, r, now)
		return err
	})
	return v, err
}
