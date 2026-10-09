package app

import (
	"context"
	"errors"
	"time"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

// Typed outcomes of a waiting Claim.
var (
	ErrDone    = fault.New(fault.CodeDone, "every executable task is complete")
	ErrStalled = fault.New(fault.CodeStalled, "open tasks remain but nothing can proceed without intervention")
)

// ClaimRequest selects work. TaskID picks a specific task (eligibility is
// still enforced); nil lets the engine choose the oldest claimable task.
type ClaimRequest struct {
	ProjectID project.ID
	TaskID    *task.ID
	// Wait blocks until work is claimable, the project is done (ErrDone),
	// nothing can proceed (ErrStalled), or ctx ends. While waiting, the
	// caller runs any pending cohort verification job, since it has
	// capacity and the job must not depend on a hidden extra command.
	Wait  bool
	Lease time.Duration
}

// Claim atomically starts an execution attempt and returns its session.
// Eligibility, generation advance, attempt insert, and the handoff read
// happen in one immediate transaction. The attempt's worktree is created
// after the transaction (Git never runs inside one) and recorded by a
// second short transaction.
func (e *Engine) Claim(ctx context.Context, req ClaimRequest) (Session, error) {
	lease, err := execution.LeaseDuration(req.Lease)
	if err != nil {
		return Session{}, err
	}
	if req.TaskID == nil && req.ProjectID == "" {
		return Session{}, fault.New(fault.CodeNoProject, "automatic claim needs a project")
	}
	if !req.Wait {
		return e.claimOnce(ctx, req, lease)
	}
	for {
		if req.ProjectID != "" {
			if _, err := e.RunPendingCohorts(ctx, req.ProjectID); err != nil && ctx.Err() != nil {
				return Session{}, ctx.Err()
			}
		}
		wake := e.changes() // subscribe before checking so no change is missed
		s, err := e.claimOnce(ctx, req, lease)
		if err == nil {
			return s, nil
		}
		if !retryable(err) {
			return Session{}, err
		}
		if req.ProjectID != "" {
			sum, serr := e.Summary(ctx, req.ProjectID)
			if serr != nil {
				return Session{}, serr
			}
			if sum.Done {
				return Session{}, ErrDone
			}
			if sum.Stalled {
				return Session{}, &fault.Error{Code: fault.CodeStalled, Message: ErrStalled.Message, Details: sum}
			}
		}
		select {
		case <-ctx.Done():
			return Session{}, ctx.Err()
		case <-wake:
		case <-time.After(e.poll):
		}
	}
}

func retryable(err error) bool {
	switch fault.CodeOf(err) {
	case fault.CodeNoAvailableTask, fault.CodeTaskBlocked, fault.CodeTaskAlreadyTaken, fault.CodeTaskAwaitingVerification, fault.CodeTaskAwaitingIntegration:
		return true
	}
	return false
}

func (e *Engine) claimOnce(ctx context.Context, req ClaimRequest, lease time.Duration) (Session, error) {
	if err := e.reconcile(ctx); err != nil {
		return Session{}, err
	}
	now := e.now()
	token := execution.NewToken()
	var sess Session
	var proj project.Project
	var attempt execution.Attempt
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		var r sqlite.Record
		var err error
		if req.TaskID != nil {
			if r, err = tx.GetRecord(*req.TaskID); err != nil {
				return err
			}
			if proj, err = tx.GetProject(r.Task.ProjectID); err != nil {
				return err
			}
			if len(proj.Regression) == 0 {
				return fault.New(fault.CodeMissingVerification, "project %s has no regression checks; define them with `at project --regression-check` before claiming work", proj.ID)
			}
			if err := notClaimable(r, proj, now); err != nil {
				return err
			}
		} else {
			if proj, err = tx.GetProject(req.ProjectID); err != nil {
				return err
			}
			if len(proj.Regression) == 0 {
				return fault.New(fault.CodeMissingVerification, "project %s has no regression checks; define them with `at project --regression-check` before claiming work", proj.ID)
			}
			cand, ok, err := tx.ClaimCandidate(req.ProjectID, now, proj.MaxAttempts)
			if err != nil {
				return err
			}
			if !ok {
				return fault.New(fault.CodeNoAvailableTask, "no claimable task in project %s", req.ProjectID)
			}
			r = cand
			// The SQL pre-filter must agree with the domain rule; a
			// disagreement is a bug, never a silent wrong claim.
			if err := notClaimable(r, proj, now); err != nil {
				return fault.Wrap(err, fault.CodeInternal, "candidate selection disagreed with status rules")
			}
		}
		attempt, err = tx.StartAttempt(r.Task.ID, r.Attempt, token.Digest(), now, now.Add(lease))
		if err != nil {
			return err
		}
		r2, err := tx.GetRecord(r.Task.ID)
		if err != nil {
			return err
		}
		view, err := e.buildView(tx, r2, proj, now, false)
		if err != nil {
			return err
		}
		handoff := *view.Handoff
		view.Handoff = nil
		if err := e.inheritContext(tx, r2, &handoff); err != nil {
			return err
		}
		sess = Session{Task: view, Token: token, AttemptSeq: attempt.Seq, LeaseUntil: attempt.LeaseExpiresAt, Resumed: attempt.Seq > 1, Handoff: handoff}
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	e.notify()
	// One branch and worktree per task, reused across attempts so committed
	// and uncommitted work survives a crash; the token lives in the
	// worktree's private git dir so commands run there need no AT_SESSION.
	mgr := e.manager(proj)
	info, werr := mgr.EnsureTask(ctx, string(sess.Task.ID), proj.TargetBranch)
	if werr == nil {
		werr = workspace.StoreToken(ctx, info.Path, string(token))
	}
	if werr != nil {
		_ = e.store.Write(ctx, func(tx *sqlite.Tx) error { return tx.EndAttempt(attempt.ID, execution.EndReleased, e.now()) })
		e.notify()
		return Session{}, werr
	}
	if err := e.store.Write(ctx, func(tx *sqlite.Tx) error { return tx.SetWorkspace(attempt.ID, info.Path, info.Branch) }); err != nil {
		return Session{}, err
	}
	sess.Workspace, sess.Branch, sess.WorkspaceDirty = info.Path, info.Branch, info.Dirty
	if sess.Task.Attempt != nil {
		sess.Task.Attempt.Workspace, sess.Task.Attempt.Branch = info.Path, info.Branch
	}
	return sess, nil
}

// inheritContext adds prerequisite learnings and the last failure to a
// handoff, bounded so the claim payload stays small.
func (e *Engine) inheritContext(tx *sqlite.Tx, r sqlite.Record, h *execution.Handoff) error {
	ids, err := tx.RequirementIDs(r.Task.ID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if len(h.Inherited) >= execution.InheritedLearningLimit {
			break
		}
		learned, err := tx.Learnings(id, execution.InheritedLearningLimit-len(h.Inherited))
		if err != nil {
			return err
		}
		if len(learned) == 0 {
			continue
		}
		key := ""
		if rec, err := tx.GetRecord(id); err == nil {
			key = rec.Task.Key
		}
		for _, l := range learned {
			h.Inherited = append(h.Inherited, execution.InheritedLearning{TaskID: string(id), Key: key, Learned: l})
		}
	}
	if s := r.Submission; s != nil && s.Run != nil && (s.Run.Status == sqlite.RunFailed || s.Run.Status == sqlite.RunError) {
		ev, err := tx.EvidenceForRun(s.Run.ID)
		if err != nil {
			return err
		}
		f := &execution.Failure{RunID: s.Run.ID, Mode: string(s.Run.Mode), Summary: s.Run.Summary}
		for _, row := range ev {
			if row.Outcome == verification.OutcomePassed || row.Outcome == verification.OutcomeSkipped {
				continue
			}
			f.Checks = append(f.Checks, execution.FailedCheck{CheckID: row.CheckID, Outcome: string(row.Outcome), Message: row.Message, StderrTail: execution.Tail(row.Stderr, execution.FailureTailBytes), StdoutTail: execution.Tail(row.Stdout, execution.FailureTailBytes)})
		}
		h.LastFailure = f
	}
	return nil
}

// notClaimable maps a non-claimable status to the error an agent sees.
func notClaimable(r sqlite.Record, proj project.Project, now time.Time) error {
	st := r.Status(now, proj.MaxAttempts)
	if st.Claimable() {
		return nil
	}
	id := r.Task.ID
	switch st {
	case task.StatusGroup:
		return fault.New(fault.CodeInvalidInput, "%s is a group and cannot be claimed", id)
	case task.StatusArchived:
		return fault.New(fault.CodeNotFound, "%s is archived", id)
	case task.StatusBlocked:
		return fault.New(fault.CodeTaskBlocked, "%s is blocked by %d incomplete prerequisite(s); see `at show %s`", id, r.UnmetRequires, id)
	case task.StatusClaimed:
		return fault.New(fault.CodeTaskAlreadyTaken, "%s is claimed by attempt %d until %s", id, r.Attempt.Seq, r.Attempt.LeaseExpiresAt.Format(time.RFC3339))
	case task.StatusVerifying, task.StatusAwaitingVerification:
		return fault.New(fault.CodeTaskAwaitingVerification, "%s has a submission being verified", id)
	case task.StatusAwaitingIntegration:
		return fault.New(fault.CodeTaskAwaitingIntegration, "%s is verified and awaiting integration", id)
	case task.StatusComplete:
		return fault.New(fault.CodeTaskComplete, "%s is complete", id)
	case task.StatusNeedsAttention:
		return fault.New(fault.CodeStalled, "%s exhausted its attempts (%d failures); a planner must `at update %s --reset-attempts`", id, r.Failures, id)
	case task.StatusCooldown:
		return fault.New(fault.CodeNoAvailableTask, "%s is in retry cooldown until %s", id, r.NextEligibleAt.Format(time.RFC3339))
	}
	return fault.New(fault.CodeInternal, "%s is in unexpected status %s", id, st)
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

// touch authorises and renews the lease: every session-authenticated write
// is proof of life, so an active agent never has to renew by hand.
func (e *Engine) touch(tx *sqlite.Tx, token execution.Token, now time.Time) (execution.Attempt, error) {
	a, err := e.authorize(tx, token, now)
	if err != nil {
		return a, err
	}
	expires := now.Add(execution.DefaultLease)
	if expires.After(a.LeaseExpiresAt) {
		if err := tx.RenewLease(a.ID, expires); err != nil {
			return a, err
		}
		a.LeaseExpiresAt = expires
	}
	return a, nil
}

// Renew extends the lease of a live session and returns the new expiry.
func (e *Engine) Renew(ctx context.Context, token execution.Token) (time.Time, error) {
	now := e.now()
	expires := now.Add(execution.DefaultLease)
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		a, err := e.authorize(tx, token, now)
		if err != nil {
			return err
		}
		return tx.RenewLease(a.ID, expires)
	})
	return expires, err
}

// ReleaseOptions controls Release.
type ReleaseOptions struct {
	Note string
	// Failed records a failed attempt (retry bookkeeping and cooldown).
	Failed bool
}

// Release gives a task back without submitting. The handoff survives and
// the task is claimable again at once (or blocked, if a prerequisite was
// added meanwhile).
func (e *Engine) Release(ctx context.Context, token execution.Token, opts ReleaseOptions) (TaskView, error) {
	now := e.now()
	var v TaskView
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		a, err := e.authorize(tx, token, now)
		if err != nil {
			return err
		}
		if entry := (execution.LogEntry{Note: opts.Note}); entry.Validate() == nil {
			if _, err := tx.InsertLog(a.TaskID, a.ID, entry, now); err != nil {
				return err
			}
		}
		if err := tx.EndAttempt(a.ID, execution.EndReleased, now); err != nil {
			return err
		}
		r, err := tx.GetRecord(a.TaskID)
		if err != nil {
			return err
		}
		proj, err := tx.GetProject(r.Task.ProjectID)
		if err != nil {
			return err
		}
		if opts.Failed {
			if err := tx.RecordFailure(a.TaskID, proj.RetryCooldown, now); err != nil {
				return err
			}
			if r, err = tx.GetRecord(a.TaskID); err != nil {
				return err
			}
		}
		v, err = e.buildView(tx, r, proj, now, false)
		return err
	})
	if err == nil {
		e.notify()
	}
	return v, err
}

// Log appends an entry under a session's authority.
func (e *Engine) Log(ctx context.Context, token execution.Token, entry execution.LogEntry) (execution.RecordedLog, error) {
	if err := entry.Validate(); err != nil {
		return execution.RecordedLog{}, err
	}
	now := e.now()
	var rec execution.RecordedLog
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		a, err := e.touch(tx, token, now)
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

// Whoami resolves a token to its task without mutating anything.
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
		proj, err := tx.GetProject(r.Task.ProjectID)
		if err != nil {
			return err
		}
		v, err = e.buildView(tx, r, proj, now, false)
		return err
	})
	return v, err
}

// heartbeat renews a session periodically while a long operation runs,
// so an expired claim never finalises over a successor. It stops on the
// first failure; the operation's own fenced writes then fail too.
func (e *Engine) heartbeat(ctx context.Context, token execution.Token) (stop func()) {
	hctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(execution.HeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-hctx.Done():
				return
			case <-t.C:
				if _, err := e.Renew(hctx, token); err != nil && !errors.Is(err, context.Canceled) {
					return
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}
