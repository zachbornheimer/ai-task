package app

import (
	"context"
	"fmt"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/proof"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

// completedTask is a finished task and the attempt that finished it.
type completedTask struct {
	prepared
	revision string
}

// completedBy resolves the token to its attempt's task, which must be
// complete. It needs no live lease: completion ended the attempt.
func (e *Engine) completedBy(ctx context.Context, token execution.Token) (completedTask, error) {
	var out completedTask
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		auth, err := tx.AttemptByDigest(token.Digest())
		if err != nil {
			return err
		}
		out.attempt = auth.Attempt
		if out.rec, err = tx.GetRecord(auth.Attempt.TaskID); err != nil {
			return err
		}
		if out.proj, err = tx.GetProject(out.rec.Task.ProjectID); err != nil {
			return err
		}
		if out.rec.CompletedAt == nil {
			return nil
		}
		subs, err := tx.Submissions(out.rec.Task.ID)
		if err != nil {
			return err
		}
		for _, s := range subs {
			if s.ID == out.rec.CompletedSubmissionID {
				out.revision = s.Revision
			}
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	out.policy = effectivePolicy(out.rec.Task, out.proj)
	out.dir = out.attempt.WorkspacePath
	return out, nil
}

// runCompletionActions runs every pending or failed action of the task the
// token completed, in declaration order, and returns all of its actions.
// A succeeded or detached action is never run again. Failures are recorded
// on the action; they never undo completion.
func (e *Engine) runCompletionActions(ctx context.Context, token execution.Token) ([]proof.ActionResult, error) {
	c, err := e.completedBy(ctx, token)
	if err != nil {
		return nil, err
	}
	id := c.rec.Task.ID
	acts, err := e.actionsOf(ctx, id)
	if err != nil {
		return nil, err
	}
	runner := e.runner()
	for i, a := range acts {
		if !a.Status.Retryable() {
			continue
		}
		if err := ctx.Err(); err != nil {
			return acts, fault.WrapInternal(err, "actions of %s interrupted", id)
		}
		if err := e.store.Write(ctx, func(tx *sqlite.Tx) error { return tx.StartAction(id, i) }); err != nil {
			return acts, err
		}
		status, run := executeAction(ctx, runner, a.Spec, c.dir)
		if err := e.store.Write(ctx, func(tx *sqlite.Tx) error { return tx.FinishAction(id, i, status, run) }); err != nil {
			return acts, err
		}
	}
	return e.actionsOf(ctx, id)
}

// executeAction runs one action: waited under the on-complete budget, or
// spawned and left running.
func executeAction(ctx context.Context, runner *proof.Runner, spec proof.ActionSpec, dir string) (proof.ActionStatus, proof.RunResult) {
	if spec.Detach {
		run := runner.Start(proof.CheckOnComplete, spec.Proof(), dir)
		if run.Passed {
			return proof.ActionDetached, run
		}
		return proof.ActionFailed, run
	}
	run := runner.Run(ctx, proof.CheckOnComplete, spec.Proof(), dir)
	if run.Passed {
		return proof.ActionSucceeded, run
	}
	return proof.ActionFailed, run
}

func (e *Engine) actionsOf(ctx context.Context, id task.ID) ([]proof.ActionResult, error) {
	var out []proof.ActionResult
	err := e.store.Read(ctx, func(tx *sqlite.Tx) (err error) {
		out, err = tx.Actions(id)
		return err
	})
	return out, err
}

// attachActions runs the completion's pending actions and reports them on
// the result. An action that fails is named in the message; the result
// stays a success because completion precedes the actions.
func (e *Engine) attachActions(ctx context.Context, token execution.Token, res *VerifyResult) error {
	acts, err := e.runCompletionActions(ctx, token)
	res.Actions = acts
	if err != nil {
		return err
	}
	failed := 0
	for _, a := range acts {
		if a.Status == proof.ActionFailed {
			failed++
		}
	}
	if failed > 0 {
		res.Message += fmt.Sprintf("; %d on-complete action(s) failed (the task stays complete; fix the cause and run `atask verify complete` again to retry only those)", failed)
	}
	return nil
}

// Reverify runs the task's proof and regression again against the revision
// that completed it, in disposable snapshots, and records the new
// RunResults. It never undoes completion and never re-runs actions.
func (e *Engine) Reverify(ctx context.Context, token execution.Token) (VerifyResult, error) {
	c, err := e.completedBy(ctx, token)
	if err != nil {
		return VerifyResult{}, err
	}
	id := c.rec.Task.ID
	if c.rec.CompletedAt == nil || c.revision == "" {
		return VerifyResult{}, fault.New(fault.CodeInvalidInput, "%s is not complete: there is no recorded revision to reverify; use `atask verify complete`", id)
	}
	res := VerifyResult{TaskID: id, Mode: verification.ModeComplete, Revision: c.revision, Status: task.StatusComplete, Completed: true, Passed: true}
	mgr := e.manager(c.proj)
	inPlace := c.proj.IsGeneral() || c.proj.RootPath == ""
	var changed []string
	if !inPlace {
		changed, _ = workspace.ChangedBetween(ctx, mgr.Repo, "refs/heads/"+c.proj.TargetBranch, c.revision)
	}
	taskOpts, regOpts := e.suiteOpts(c.rec.Task, c.proj, changed)
	for _, mode := range []verification.Mode{verification.ModeProof, verification.ModeRegression} {
		policy := c.policy.Subset(mode)
		opts := taskOpts
		if mode == verification.ModeRegression {
			opts = regOpts
		}
		ev, runStatus, err := e.reverifyOne(ctx, mgr, c, policy, mode, opts, inPlace)
		res.Evidence = append(res.Evidence, ev...)
		if err != nil {
			return res, err
		}
		if runStatus != sqlite.RunPassed {
			res.Passed = false
		}
	}
	res.Summary = "reverified revision " + short(c.revision)
	res.Message = "reverified against the recorded revision; the task stays complete"
	if !res.Passed {
		res.Message = "reverify FAILED against the recorded revision; the task stays complete, but its proof no longer holds"
		return res, &fault.Error{Code: fault.CodeCheckFailed, Message: fmt.Sprintf("%s: %s", id, res.Message), Details: res}
	}
	return res, nil
}

// reverifyOne runs one category (proof or regression) as its own recorded
// run on a snapshot of the completed revision.
func (e *Engine) reverifyOne(ctx context.Context, mgr workspace.Manager, c completedTask, policy verification.Policy, mode verification.Mode, opts runOpts, inPlace bool) ([]verification.Evidence, sqlite.RunStatus, error) {
	gate := policy.TaskChecks
	if mode == verification.ModeRegression {
		gate = policy.Regression
	}
	if len(gate) == 0 {
		return nil, sqlite.RunPassed, nil
	}
	id := c.rec.Task.ID
	var runID int64
	if err := e.store.Write(ctx, func(tx *sqlite.Tx) (err error) {
		runID, err = tx.InsertRun(sqlite.NewRun{TaskID: id, AttemptID: c.attempt.ID, Mode: mode, Status: sqlite.RunRunning, Revision: c.revision, Policy: policy, Environment: environmentFingerprint()}, e.now())
		return err
	}); err != nil {
		return nil, sqlite.RunError, err
	}
	dir, cleanup := c.dir, func() {}
	if !inPlace {
		var err error
		if dir, cleanup, err = mgr.Snapshot(ctx, c.revision); err != nil {
			_ = e.store.Write(ctx, func(tx *sqlite.Tx) error { return tx.FinishRun(runID, sqlite.RunError, err.Error(), e.now()) })
			return nil, sqlite.RunError, err
		}
	}
	defer cleanup()
	opts.logDir = e.runLogDir(c.proj.ID, runID)
	noFence := func(*sqlite.Tx) error { return nil }
	ev, err := e.runChecks(ctx, dir, policy, runID, c.revision, noFence, opts)
	if err != nil {
		_ = e.store.Write(ctx, func(tx *sqlite.Tx) error { return tx.FinishRun(runID, sqlite.RunError, err.Error(), e.now()) })
		return ev, sqlite.RunError, err
	}
	verdict := verification.Judge(policy, ev)
	status := sqlite.RunFailed
	if verdict.Passed {
		status = sqlite.RunPassed
	}
	err = e.store.Write(ctx, func(tx *sqlite.Tx) error {
		return tx.FinishRun(runID, status, "reverify: "+verdict.Summary, e.now())
	})
	return ev, status, err
}
