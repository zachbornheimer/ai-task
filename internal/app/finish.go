package app

import (
	"context"
	"time"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

// FinishOptions controls Finish.
type FinishOptions struct {
	// NoVerify records the submission without running checks; a later
	// `tasks verify` runs them.
	NoVerify bool
}

// Finish submits the attempt's implementation for the completion process.
//
// The submission is recorded in one transaction (ending the attempt) before
// any check runs, so a crash during verification never loses the
// submission. If the project directory is a Git repository, the working
// tree must be clean and HEAD becomes the submission's immutable revision;
// nothing is staged or committed on the agent's behalf. If the effective
// policy requires nothing, the submission is vacuously verified and, under
// integration policy "none", the task is complete. Otherwise checks run
// synchronously (unless NoVerify) and the result reports the verdict; a
// failed verdict is returned as VERIFICATION_FAILED with the result in
// Details. A task is never completed on an agent's word alone.
//
// Finish is idempotent per session: calling it again with the same token
// returns the existing submission.
func (e *Engine) Finish(ctx context.Context, token execution.Token, opts FinishOptions) (SubmissionResult, error) {
	// Phase 0 (read): find the project directory so the revision can be
	// captured outside any write transaction.
	var root string
	var taskID task.ID
	now0 := e.now()
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		auth, err := tx.AttemptByDigest(token.Digest())
		if err != nil {
			return err
		}
		taskID = auth.Attempt.TaskID
		r, err := tx.GetRecord(taskID)
		if err != nil {
			return err
		}
		// Report an authority problem before any workspace problem; the
		// write transaction re-checks authority atomically later.
		replay := auth.Attempt.EndedAt != nil && auth.Attempt.EndReason == execution.EndFinished && r.Submission != nil && r.Submission.AttemptID == auth.Attempt.ID
		if !replay {
			if err := execution.Authorize(auth.Attempt, auth.CurrentSeq, now0); err != nil {
				return err
			}
		}
		proj, err := tx.GetProject(r.Task.ProjectID)
		if err != nil {
			return err
		}
		root = proj.RootPath
		return nil
	})
	if err != nil {
		return SubmissionResult{}, err
	}
	revision := ""
	if root != "" && workspace.IsGitRepo(root) {
		st, err := workspace.RequireClean(ctx, root)
		if err != nil {
			// Do not fail an idempotent replay on a dirty tree: the
			// submission already exists. Fall through to the transaction,
			// which handles replay before authorising a new submission.
			if replay, ok := e.replayFinish(ctx, token); ok {
				return replay, nil
			}
			return SubmissionResult{}, err
		}
		revision = st.Revision
	}

	now := e.now()
	var res SubmissionResult
	needsVerify := false
	err = e.store.Write(ctx, func(tx *sqlite.Tx) error {
		auth, err := tx.AttemptByDigest(token.Digest())
		if err != nil {
			return err
		}
		a := auth.Attempt
		r, err := tx.GetRecord(a.TaskID)
		if err != nil {
			return err
		}
		if a.EndedAt != nil && a.EndReason == execution.EndFinished && r.Submission != nil && r.Submission.AttemptID == a.ID {
			res = resultFrom(r, now)
			res.AlreadySubmitted = true
			res.Message = "submission already recorded for this session"
			return nil
		}
		if err := execution.Authorize(a, auth.CurrentSeq, now); err != nil {
			return err
		}
		proj, err := tx.GetProject(r.Task.ProjectID)
		if err != nil {
			return err
		}
		policy := verification.Merge(r.Task.Verification, proj.Regression)
		sid, err := tx.InsertSubmission(a.TaskID, a.ID, revision, now)
		if err != nil {
			return err
		}
		if err := tx.EndAttempt(a.ID, execution.EndFinished, now); err != nil {
			return err
		}
		if policy.Empty() {
			// Nothing to run: record a passed run so the evidence trail
			// states explicitly that no checks were required.
			if _, err := tx.InsertRun(sid, sqlite.RunPassed, policy, environmentFingerprint(), "no checks required by policy "+policy.Digest(), now); err != nil {
				return err
			}
			if proj.Integration == project.IntegrationNone {
				if err := tx.MarkComplete(a.TaskID, sid, now); err != nil {
					return err
				}
			}
		} else {
			if _, err := tx.InsertRun(sid, sqlite.RunPending, policy, "", "awaiting check execution", now); err != nil {
				return err
			}
			needsVerify = true
		}
		r2, err := tx.GetRecord(a.TaskID)
		if err != nil {
			return err
		}
		res = resultFrom(r2, now)
		switch res.Status {
		case task.StatusComplete:
			res.Message = "task complete"
		case task.StatusAwaitingVerification:
			res.Message = "submission recorded; required checks have not run yet"
		}
		return nil
	})
	if err != nil {
		return SubmissionResult{}, err
	}
	if !needsVerify || opts.NoVerify {
		return res, nil
	}
	// Phase 2: run the checks (outside any transaction).
	vr, verr := e.Verify(ctx, taskID, VerifyOptions{})
	if verr != nil {
		var fe *fault.Error
		if asFault(verr, &fe) && fe.Code == fault.CodeVerificationFailed {
			res.RunID, res.PolicyDigest, res.Verification, res.Summary, res.Status = vr.RunID, vr.PolicyDigest, vr.Verification, vr.Summary, vr.Status
			res.Evidence = vr.Evidence
			res.Message = "submission recorded; verification failed"
			fe.Details = res
		}
		return res, verr
	}
	res.RunID, res.PolicyDigest, res.Verification, res.Summary, res.Status = vr.RunID, vr.PolicyDigest, vr.Verification, vr.Summary, vr.Status
	res.Evidence = vr.Evidence
	res.Message = "submission verified"
	if res.Status == task.StatusComplete {
		res.Message = "submission verified; task complete"
	}
	return res, nil
}

// replayFinish returns the existing result when the token's attempt has
// already finished.
func (e *Engine) replayFinish(ctx context.Context, token execution.Token) (SubmissionResult, bool) {
	now := e.now()
	var res SubmissionResult
	ok := false
	_ = e.store.Read(ctx, func(tx *sqlite.Tx) error {
		auth, err := tx.AttemptByDigest(token.Digest())
		if err != nil {
			return err
		}
		a := auth.Attempt
		r, err := tx.GetRecord(a.TaskID)
		if err != nil {
			return err
		}
		if a.EndedAt != nil && a.EndReason == execution.EndFinished && r.Submission != nil && r.Submission.AttemptID == a.ID {
			res = resultFrom(r, now)
			res.AlreadySubmitted = true
			res.Message = "submission already recorded for this session"
			ok = true
		}
		return nil
	})
	return res, ok
}

func resultFrom(r sqlite.Record, now time.Time) SubmissionResult {
	s := r.Submission
	if s == nil {
		return SubmissionResult{TaskID: r.Task.ID, Status: r.Status(now)}
	}
	res := SubmissionResult{
		TaskID: r.Task.ID, SubmissionID: s.ID, AttemptSeq: s.AttemptSeq, Revision: s.Revision,
		Verification: s.RunStatus(), Status: r.Status(now),
	}
	if s.Run != nil {
		res.RunID = s.Run.ID
		res.PolicyDigest = s.Run.PolicyDigest
		res.Summary = s.Run.Summary
	}
	return res
}
