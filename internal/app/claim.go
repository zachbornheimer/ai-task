package app

import (
	"context"
	"errors"
	"strings"
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
// GroupID restricts that choice to the executable tasks beneath a group, at
// any depth: each member is claimed in its own project, the answers DONE,
// STALLED, and NOT_READY describe that subtree only, and ProjectID is
// ignored (the group's own project applies). It excludes TaskID.
type ClaimRequest struct {
	ProjectID project.ID
	TaskID    *task.ID
	GroupID   task.ID
	// Wait blocks until work is claimable, the project (or group) is done
	// (ErrDone), nothing can proceed (ErrStalled), or ctx ends. While
	// waiting, the caller runs any pending cohort verification job, since
	// it has capacity and the job must not depend on a hidden extra
	// command. A group wait runs only that group's pending proofs.
	Wait bool
}

// Claim atomically starts an execution attempt and returns its session.
// Eligibility, generation advance, attempt insert, and the handoff read
// happen in one immediate transaction. The attempt's worktree is created
// after the transaction (Git never runs inside one) and recorded by a
// second short transaction.
func (e *Engine) Claim(ctx context.Context, req ClaimRequest) (Session, error) {
	lease := e.lease
	switch {
	case req.GroupID != "":
		if req.TaskID != nil {
			return Session{}, fault.New(fault.CodeInvalidInput, "a claim names a task or a group, not both")
		}
		pid, err := e.groupHome(ctx, req.GroupID)
		if err != nil {
			return Session{}, err
		}
		req.ProjectID = pid
	case req.TaskID == nil && req.ProjectID == "":
		if _, err := e.EnsureGeneral(ctx); err != nil {
			return Session{}, err
		}
	}
	if !req.Wait {
		s, err := e.claimOnce(ctx, req, lease)
		if err == errNoClaimable && req.GroupID != "" { //nolint:errorlint // the sentinel is returned as is; errors.Is matches by code
			return Session{}, e.groupIdle(ctx, req.GroupID)
		}
		return s, err
	}
	for {
		switch {
		case req.GroupID != "":
			if _, err := e.runPendingGroups(ctx, req.ProjectID, "", req.GroupID); err != nil && ctx.Err() != nil {
				return Session{}, ctx.Err()
			}
		case req.ProjectID != "":
			if _, err := e.RunPendingGroups(ctx, req.ProjectID, ""); err != nil && ctx.Err() != nil {
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
		wait := e.poll
		var sum Summary
		var serr error
		scoped := true
		switch {
		case req.GroupID != "":
			sum, _, serr = e.groupSummary(ctx, req.GroupID)
		case req.ProjectID != "" || req.TaskID == nil: // no project: the unscoped board
			sum, serr = e.Summary(ctx, req.ProjectID)
		default:
			scoped = false
		}
		if scoped {
			if serr != nil {
				return Session{}, serr
			}
			if sum.Done {
				return Session{}, ErrDone
			}
			if sum.Stalled {
				return Session{}, &fault.Error{Code: fault.CodeStalled, Message: ErrStalled.Message, Details: sum}
			}
			// A cooldown resolves itself: wait for it, no later than its
			// expiry, rather than report a stall.
			if sum.NextEligibleAt != nil {
				if d := sum.NextEligibleAt.Sub(e.now()); d > 0 && d < wait {
					wait = d
				}
			}
		}
		select {
		case <-ctx.Done():
			return Session{}, ctx.Err()
		case <-wake:
		case <-time.After(wait):
		}
	}
}

func retryable(err error) bool {
	switch fault.CodeOf(err) {
	case fault.CodeNotReady, fault.CodeConflict, fault.CodeBaseConflict:
		// Not takeable right now, or the failed task is now excluded from
		// selection: keep scanning. Infrastructure failures stop the scan.
		return !fault.IsInternal(err)
	}
	return false
}

func (e *Engine) claimOnce(ctx context.Context, req ClaimRequest, lease time.Duration) (Session, error) {
	if err := e.reconcile(ctx); err != nil {
		return Session{}, err
	}
	if req.TaskID != nil {
		if s, ok := e.liveClaimFromCwd(ctx, *req.TaskID); ok {
			return s, nil
		}
	}
	now := e.now()
	token := execution.NewToken()
	var sess Session
	var proj project.Project
	var attempt execution.Attempt
	quarantineSeq := 0
	var required []workspace.Requirement
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
			// An explicit claim is the retry path for a task whose worktree
			// could not be prepared: the caller decided to try again.
			if err := notClaimable(r, proj, now, true); err != nil {
				return err
			}
		} else {
			maxAttempts := project.DefaultMaxAttempts
			if req.ProjectID != "" {
				if proj, err = tx.GetProject(req.ProjectID); err != nil {
					return err
				}
				maxAttempts = proj.MaxAttempts
			}
			var cand sqlite.Record
			var ok bool
			if req.GroupID != "" {
				cand, ok, err = tx.GroupClaimCandidate(req.GroupID, now, maxAttempts)
			} else {
				cand, ok, err = tx.ClaimCandidate(req.ProjectID, now, maxAttempts)
			}
			if err != nil {
				return err
			}
			if !ok {
				return errNoClaimable
			}
			r = cand
			if proj, err = tx.GetProject(r.Task.ProjectID); err != nil {
				return err
			}
			// The SQL pre-filter must agree with the domain rule; a
			// disagreement is a bug, never a silent wrong claim.
			if err := notClaimable(r, proj, now, false); err != nil {
				return fault.WrapInternal(err, "candidate selection disagreed with status rules")
			}
		}
		// A rootless attempt runs in the claiming directory, and its checks
		// get <dir>.tmp as TMPDIR: refuse the claim now rather than let
		// every check that makes a temp file fail later.
		if proj.Rootless() {
			if err := ensureCheckTmp(e.cwd); err != nil {
				return err
			}
		}
		// A previous attempt that never ended (lease ran out) may still have
		// a live process in the worktree: quarantine that worktree.
		if prev := r.Attempt; prev != nil && prev.EndedAt == nil {
			quarantineSeq = prev.Seq
		}
		if required, err = completedRequirements(tx, r.Task.ID); err != nil {
			return err
		}
		attempt, err = tx.StartAttempt(r.Task.ID, r.Attempt, token.Digest(), now, lease)
		if err != nil {
			return err
		}
		r2, err := tx.GetRecord(r.Task.ID)
		if err != nil {
			return err
		}
		view, err := e.buildView(tx, r2, proj, now, false, targetHead{})
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
	if proj.Rootless() {
		// A rootless attempt runs in place where it was claimed: its checks
		// and on-complete actions use the claiming directory, the same
		// directory its session token is stored under.
		dir := e.cwd
		if err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
			if err := tx.SetWorkspace(attempt.ID, dir, ""); err != nil {
				return err
			}
			return tx.SetWorkspaceError(sess.Task.ID, "", e.now())
		}); err != nil {
			return Session{}, err
		}
		if err := e.storeRootlessToken(sess); err != nil {
			return Session{}, err
		}
		sess.Workspace = dir
		if sess.Task.Attempt != nil {
			sess.Task.Attempt.Workspace = dir
		}
		return sess, nil
	}
	// One branch and worktree per task, reused across clean handoffs so
	// committed and uncommitted work survives. The token is stored in the
	// worktree's private git dir so commands run there need no ATASK_SESSION.
	// When the previous attempt did not end cleanly its worktree is
	// quarantined first, so a stale process keeps only its own expired
	// token and can never reach this attempt's files or branch.
	mgr := e.manager(proj)
	base, werr := proj.TargetBranch, error(nil)
	var baseNotes []string
	if !mgr.BranchExists(ctx, workspace.TaskBranch(string(sess.Task.ID))) {
		var resolved workspace.BaseResult
		resolved, werr = mgr.ResolveBase(ctx, proj.TargetBranch, required)
		base, baseNotes = resolved.Revision, resolved.Explain()
	}
	var info workspace.Info
	if werr == nil {
		info, werr = mgr.EnsureTask(ctx, string(sess.Task.ID), base, quarantineSeq)
	}
	if werr == nil {
		werr = workspace.StoreToken(ctx, info.Path, string(token))
	}
	if werr != nil {
		// The task, not the queue, is blocked: record the reason so
		// automatic selection skips it and humans can see it, and end the
		// attempt that never got a workspace.
		_ = e.store.Write(ctx, func(tx *sqlite.Tx) error {
			now := e.now()
			if err := tx.EndAttempt(attempt.ID, execution.EndReleased, now); err != nil {
				return err
			}
			return tx.SetWorkspaceError(sess.Task.ID, fault.MessageOf(werr), now)
		})
		e.notify()
		if fault.CodeOf(werr) == fault.CodeBaseConflict {
			return Session{}, fault.New(fault.CodeBaseConflict, "%s: required tasks cannot be combined: %s; the task stays out of automatic claims until the conflict is resolved (rework one required task) and `atask claim %s` succeeds", sess.Task.ID, fault.MessageOf(werr), sess.Task.ID)
		}
		return Session{}, fault.New(fault.CodeNotReady, "%s: workspace could not be prepared (%v); the task is marked needs_attention and skipped by automatic claims until `atask claim %s` succeeds or a planner runs `atask update %s --reset-attempts`", sess.Task.ID, fault.MessageOf(werr), sess.Task.ID, sess.Task.ID)
	}
	if err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		if err := tx.SetWorkspace(attempt.ID, info.Path, info.Branch); err != nil {
			return err
		}
		if len(baseNotes) > 0 {
			entry := execution.LogEntry{Note: strings.Join(baseNotes, "\n")}
			if _, err := tx.InsertLog(sess.Task.ID, attempt.ID, entry, e.now()); err != nil {
				return err
			}
		}
		return tx.SetWorkspaceError(sess.Task.ID, "", e.now())
	}); err != nil {
		return Session{}, err
	}
	sess.Workspace, sess.Branch, sess.WorkspaceDirty, sess.QuarantinedWorkspace = info.Path, info.Branch, info.Dirty, info.Quarantined
	sess.TargetBranch, sess.Regression = proj.TargetBranch, regressionChecks(sess.Task.Regression)
	if rev, err := mgr.Revision(ctx, "refs/heads/"+proj.TargetBranch); err == nil {
		sess.TargetRevision = rev
	}
	if sess.Task.Attempt != nil {
		sess.Task.Attempt.Workspace, sess.Task.Attempt.Branch = info.Path, info.Branch
	}
	return sess, nil
}

// completedRequirements lists the completed revisions of the tasks id
// requires, in requirement order.
func completedRequirements(tx *sqlite.Tx, id task.ID) ([]workspace.Requirement, error) {
	recs, err := tx.Requirements(id)
	if err != nil {
		return nil, err
	}
	var out []workspace.Requirement
	for i := range recs {
		r := &recs[i]
		if r.CompletedRevision != "" {
			out = append(out, workspace.Requirement{Task: string(r.Task.ID), Revision: r.CompletedRevision})
		}
	}
	return out, nil
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
		for i := range ev {
			row := &ev[i]
			if row.Outcome == verification.OutcomePassed || row.Outcome == verification.OutcomeSkipped {
				continue
			}
			f.Checks = append(f.Checks, execution.FailedCheck{CheckID: row.CheckID, Outcome: string(row.Outcome), Message: row.Message, StderrTail: execution.Tail(row.Stderr, execution.FailureTailBytes), StdoutTail: execution.Tail(row.Stdout, execution.FailureTailBytes), Log: row.Log})
		}
		h.LastFailure = f
	}
	return nil
}

// notClaimable maps a non-claimable status to the error an agent sees.
// retryWorkspace lets an explicit claim try again on a task whose only
// problem is a workspace that could not be prepared.
func notClaimable(r sqlite.Record, proj project.Project, now time.Time, retryWorkspace bool) error {
	facts := r.Facts(now, proj.MaxAttempts)
	if retryWorkspace {
		facts.WorkspaceBlocked = false
	}
	st := task.Derive(facts)
	if st.Claimable() {
		return nil
	}
	id := r.Task.ID
	switch st {
	case task.StatusGroup:
		return fault.New(fault.CodeInvalidInput, "%s is a group and cannot be claimed", id)
	case task.StatusArchived:
		return fault.New(fault.CodeNotFound, "%s is archived", id)
	case task.StatusDraft:
		return fault.New(fault.CodeNotReady, "%s is a draft: it needs both proofs before it can be claimed; run `atask update %s --proof \"run: ...\" --regression \"run: ...\"`", id, id)
	case task.StatusBlocked:
		return fault.New(fault.CodeNotReady, "%s is blocked by %d incomplete prerequisite(s); see `atask show %s`", id, r.UnmetRequires, id)
	case task.StatusClaimed:
		return claimedConflict(id, r.Attempt)
	case task.StatusVerifying, task.StatusAwaitingVerification:
		return fault.New(fault.CodeNotReady, "%s has a submission being verified", id)
	case task.StatusComplete:
		return fault.New(fault.CodeConflict, "%s is complete", id)
	case task.StatusNeedsAttention:
		if facts.Exhausted {
			return fault.New(fault.CodeStalled, "%s exhausted its attempts (%d failures); a planner must `atask update %s --reset-attempts`", id, r.Failures, id)
		}
		return fault.New(fault.CodeStalled, "%s: workspace unusable (%s); fix the worktree and `atask claim %s`, or `atask update %s --reset-attempts`", id, r.WorkspaceError, id, id)
	case task.StatusCooldown:
		return fault.New(fault.CodeNotReady, "%s is in retry cooldown until %s", id, r.NextEligibleAt.Format(time.RFC3339))
	}
	return fault.NewInternal("%s is in unexpected status %s", id, st)
}

// authorize resolves a token to an attempt with live authority. It must be
// called inside the transaction that performs the mutation.
func (e *Engine) authorize(tx *sqlite.Tx, token execution.Token, now time.Time) (execution.Attempt, error) {
	auth, err := tx.AttemptByDigest(token.Digest())
	if err != nil {
		return execution.Attempt{}, err
	}
	if err := execution.Authorize(auth.Attempt, auth.CurrentSeq, now); err != nil {
		if auth.Attempt.Seq != auth.CurrentSeq {
			err = e.withStaleWorktreeHint(tx, auth.Attempt, err)
		}
		return execution.Attempt{}, err
	}
	return auth.Attempt, nil
}

// withStaleWorktreeHint extends the superseded-session refusal with where the
// attempt's quarantined worktree is and how to carry its late commits over.
// The hint is best effort: when git cannot be read the refusal stands as is.
func (e *Engine) withStaleWorktreeHint(tx *sqlite.Tx, a execution.Attempt, refusal error) error {
	var fe *fault.Error
	if !errors.As(refusal, &fe) || a.WorkspacePath == "" {
		return refusal
	}
	r, err := tx.GetRecord(a.TaskID)
	if err != nil {
		return refusal
	}
	proj, err := tx.GetProject(r.Task.ProjectID)
	if err != nil {
		return refusal
	}
	stale := workspace.QuarantinedPath(a.WorkspacePath, a.Seq)
	tip, head, err := e.manager(proj).StaleDivergence(context.Background(), string(a.TaskID), stale)
	if err != nil {
		return refusal
	}
	if tip == head {
		return fault.New(fe.Code, "%s; %s has no late commits and can be pruned", fe.Message, stale)
	}
	return fault.New(fe.Code, "%s; commits made in %s after takeover sit on a detached HEAD; carry them over with: git -C %s cherry-pick %s..%s", fe.Message, stale, a.WorkspacePath, tip, head)
}

// touch authorizes and renews the lease: every session-authenticated write
// is proof of life, so an active agent never has to renew by hand.
func (e *Engine) touch(tx *sqlite.Tx, token execution.Token, now time.Time) (execution.Attempt, error) {
	a, err := e.authorize(tx, token, now)
	if err != nil {
		return a, err
	}
	if expires := a.RenewedExpiry(now); expires.After(a.LeaseExpiresAt) {
		if err := tx.RenewLease(a.ID, expires); err != nil {
			return a, err
		}
		a.LeaseExpiresAt = expires
	}
	return a, nil
}

// Renew extends the lease of a live session by the attempt's own lease
// length and returns the new expiry. It never shortens a lease.
func (e *Engine) Renew(ctx context.Context, token execution.Token) (time.Time, error) {
	now := e.now()
	var expires time.Time
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		a, err := e.touch(tx, token, now)
		if err != nil {
			return err
		}
		expires = a.LeaseExpiresAt
		return nil
	})
	return expires, err
}

// ReleaseRequest names the claim to give back. Failed charges the attempt
// against the task's budget (retry bookkeeping and cooldown); without it the
// attempt is not counted.
type ReleaseRequest struct {
	Token  execution.Token
	Note   string
	Failed bool
}

// Release gives a task back without submitting; see ReleaseTask.
func (e *Engine) Release(ctx context.Context, q ReleaseRequest) error {
	_, err := e.ReleaseTask(ctx, q)
	return err
}

// ReleaseTask is Release that also returns the task as it stands afterwards. The handoff survives and
// the task is claimable again at once (or blocked, if a prerequisite was
// added meanwhile).
func (e *Engine) ReleaseTask(ctx context.Context, q ReleaseRequest) (TaskView, error) {
	now := e.now()
	var v TaskView
	var note string
	var released task.ID
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		a, expired, err := e.authorizeRelease(tx, q.Token, now)
		if err != nil {
			return err
		}
		released = a.TaskID
		if expired {
			note = "lease had already expired; the task was already unowned and is released"
		}
		if entry := (execution.LogEntry{Note: q.Note}); entry.Validate() == nil {
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
		if q.Failed {
			if _, err := tx.RecordFailure(a.TaskID, a.ID, proj.RetryCooldown, now); err != nil {
				return err
			}
			if r, err = tx.GetRecord(a.TaskID); err != nil {
				return err
			}
		}
		v, err = e.buildView(tx, r, proj, now, false, targetHead{})
		v.Note = note
		return err
	})
	if err == nil {
		e.sessions().RemoveTask(string(released))
		e.notify()
	}
	return v, err
}

// authorizeRelease is authorize, except that an expired lease on the current
// attempt is accepted: the task is already unowned, so giving it back is
// idempotent. expired reports that case.
func (e *Engine) authorizeRelease(tx *sqlite.Tx, token execution.Token, now time.Time) (a execution.Attempt, expired bool, err error) {
	a, err = e.authorize(tx, token, now)
	if fault.CodeOf(err) != fault.CodeInvalidSession {
		return a, false, err
	}
	auth, lookupErr := tx.AttemptByDigest(token.Digest())
	if lookupErr != nil {
		return a, false, lookupErr
	}
	return auth.Attempt, true, nil
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
		v, err = e.buildView(tx, r, proj, now, false, targetHead{})
		return err
	})
	return v, err
}

// heartbeat renews a session periodically while a long operation runs.
// It returns the context the operation's external work (check processes)
// must use: a terminal authority error (superseded, expired, finished)
// cancels it at once so no process keeps running for a claim that is
// gone, and a transient error (busy database) is retried sooner than the
// normal interval. stop ends the heartbeat and returns the terminal error,
// if any, so the caller can report why the work was aborted.
func (e *Engine) heartbeat(ctx context.Context, token execution.Token, lease time.Duration) (context.Context, func() error) {
	opCtx, cancelOp := context.WithCancel(ctx)
	hctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	var terminal error
	go func() {
		defer close(done)
		interval := e.renewalInterval(lease)
		t := time.NewTimer(interval)
		defer t.Stop()
		var expiry time.Time
		for {
			select {
			case <-hctx.Done():
				return
			case <-t.C:
			}
			err := e.fault("heartbeat:renew")
			if err == nil {
				expiry, err = e.Renew(hctx, token)
			}
			switch {
			case err == nil:
				t.Reset(interval)
			case errors.Is(err, context.Canceled):
				return
			case terminalAuthority(err):
				terminal = err
				cancelOp()
				return
			default:
				// Transient: retry sooner. If the lease really ran out
				// meanwhile, the next fenced write refuses anyway; stop the
				// external work now rather than let it outlive the claim.
				if !expiry.IsZero() && !e.now().Before(expiry) {
					terminal = fault.Wrap(err, fault.CodeInvalidSession, "lease renewal kept failing until the lease expired")
					cancelOp()
					return
				}
				t.Reset(interval / 5)
			}
		}
	}()
	return opCtx, func() error { cancel(); <-done; cancelOp(); return terminal }
}

// renewalInterval is how often a long operation renews a lease of the given
// length: a third of the lease, capped by the configured interval, so two
// consecutive renewals can fail before the lease runs out.
func (e *Engine) renewalInterval(lease time.Duration) time.Duration {
	if lease <= 0 {
		lease = execution.DefaultLease
	}
	return min(e.heartbeatEvery, lease/3)
}

// terminalAuthority reports errors no retry can fix.
func terminalAuthority(err error) bool {
	return fault.CodeOf(err) == fault.CodeInvalidSession
}

// claimedConflict tells a caller who lost the token where the live session is
// stored, so they resume there instead of waiting out the lease.
func claimedConflict(id task.ID, a *execution.Attempt) error {
	until := a.LeaseExpiresAt.Format(time.RFC3339)
	if a.WorkspacePath == "" {
		return fault.New(fault.CodeConflict, "%s is claimed by attempt %d until %s; the session token was printed only at claim time.", id, a.Seq, until)
	}
	return fault.New(fault.CodeConflict, "%s is claimed by attempt %d until %s in worktree %s; run atask log / atask verify from that worktree, where the session is stored.", id, a.Seq, until, a.WorkspacePath)
}
