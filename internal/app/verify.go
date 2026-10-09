package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/zachbornheimer/ai-task/internal/checkexec"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

// VerifyOptions controls Verify.
type VerifyOptions struct {
	// Retry restarts verification when the newest run is stuck in
	// "running" (the process that ran it is gone). The stuck run is closed
	// as an error and a new run starts; per-check evidence already recorded
	// at the same revision is reused.
	Retry bool
	// Again re-verifies a submission that already passed, under the current
	// effective policy. Completion is withdrawn until the new run passes.
	Again bool
}

// VerifyResult reports one verification run.
type VerifyResult struct {
	TaskID       task.ID                 `json:"task_id"`
	SubmissionID int64                   `json:"submission_id"`
	RunID        int64                   `json:"run_id"`
	Revision     string                  `json:"revision,omitempty"`
	PolicyDigest string                  `json:"policy_digest"`
	Verification sqlite.RunStatus        `json:"verification"`
	Summary      string                  `json:"summary"`
	Status       task.Status             `json:"status"`
	Evidence     []verification.Evidence `json:"evidence"`
}

// runPlan is what the first transaction hands to the executor.
type runPlan struct {
	taskID     task.ID
	submission int64
	runID      int64
	revision   string
	root       string
	policy     verification.Policy
	steps      []verification.Step
	proj       project.Project
}

// Verify executes the effective verification policy against the task's
// newest submission and records evidence. The work happens in three
// phases so that no database transaction is held while checks run:
//
//  1. one write transaction records the run as "running";
//  2. each check runs outside any transaction and its evidence commits
//     in its own short transaction as soon as it finishes;
//  3. one write transaction judges the evidence and records the verdict
//     (and the completion fact when the policy passed under integration
//     policy "none").
//
// A crash between phases leaves a "running" run that `Verify` with Retry
// closes and restarts, reusing the evidence already committed.
func (e *Engine) Verify(ctx context.Context, id task.ID, opts VerifyOptions) (VerifyResult, error) {
	plan, early, err := e.beginRun(ctx, id, opts)
	if err != nil {
		return VerifyResult{}, err
	}
	if early != nil {
		return *early, nil
	}
	if err := e.executeRun(ctx, plan); err != nil {
		return VerifyResult{}, err
	}
	return e.finishRun(ctx, plan)
}

func environmentFingerprint() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s/%s %s host=%s", runtime.GOOS, runtime.GOARCH, runtime.Version(), host)
}

func (e *Engine) beginRun(ctx context.Context, id task.ID, opts VerifyOptions) (runPlan, *VerifyResult, error) {
	now := e.now()
	var plan runPlan
	var early *VerifyResult
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		r, err := tx.GetRecord(id)
		if err != nil {
			return err
		}
		sub := r.Submission
		if sub == nil {
			return fault.New(fault.CodeNothingToVerify, "task %s has no submission; an agent must `tasks finish` first", id)
		}
		switch sub.RunStatus() {
		case sqlite.RunRunning:
			if !opts.Retry {
				started := ""
				if sub.Run.StartedAt != nil {
					started = sub.Run.StartedAt.Format(time.RFC3339)
				}
				return fault.New(fault.CodeVerificationRunning, "run %d of task %s is marked running (started %s); if that process is gone, re-run with --retry", sub.Run.ID, id, started)
			}
			if err := tx.FinishRun(sub.Run.ID, sqlite.RunError, "interrupted; superseded by a retry", now); err != nil {
				return err
			}
		case sqlite.RunPassed:
			if !opts.Again {
				return fault.New(fault.CodeNothingToVerify, "submission %d of task %s already passed verification (run %d); use --again to re-verify under the current policy", sub.ID, id, sub.Run.ID)
			}
			if r.CompletedAt != nil {
				if err := tx.ClearComplete(id, now); err != nil {
					return err
				}
			}
		}
		proj, err := tx.GetProject(r.Task.ProjectID)
		if err != nil {
			return err
		}
		policy := verification.Merge(r.Task.Verification, proj.Regression)
		env := environmentFingerprint()
		if policy.Empty() {
			runID, err := tx.InsertRun(sub.ID, sqlite.RunPassed, policy, env, "no checks required by policy "+policy.Digest(), now)
			if err != nil {
				return err
			}
			if proj.Integration == project.IntegrationNone {
				if err := tx.MarkComplete(id, sub.ID, now); err != nil {
					return err
				}
			}
			r2, err := tx.GetRecord(id)
			if err != nil {
				return err
			}
			early = &VerifyResult{TaskID: id, SubmissionID: sub.ID, RunID: runID, Revision: sub.Revision, PolicyDigest: policy.Digest(), Verification: sqlite.RunPassed, Summary: "no checks required", Status: r2.Status(now), Evidence: []verification.Evidence{}}
			return nil
		}
		var runID int64
		if sub.Run != nil && sub.Run.Status == sqlite.RunPending && sub.Run.PolicyDigest == policy.Digest() {
			// Reuse the pending row Finish recorded for this exact policy.
			runID = sub.Run.ID
			if err := tx.StartRun(runID, env, now); err != nil {
				return err
			}
		} else {
			if runID, err = tx.InsertRun(sub.ID, sqlite.RunRunning, policy, env, "", now); err != nil {
				return err
			}
		}
		plan = runPlan{taskID: id, submission: sub.ID, runID: runID, revision: sub.Revision, root: proj.RootPath, policy: policy, steps: verification.Plan(policy), proj: proj}
		return nil
	})
	return plan, early, err
}

// executeRun runs every planned step, committing each result as it lands.
func (e *Engine) executeRun(ctx context.Context, plan runPlan) error {
	requiredTaskFailed := false
	for _, step := range plan.steps {
		if err := ctx.Err(); err != nil {
			// Leave the run "running": Retry recovers it.
			return fault.Wrap(err, fault.CodeInternal, "verification interrupted; run %d can be resumed with `tasks verify %s --retry`", plan.runID, plan.taskID)
		}
		ev := e.runStep(ctx, plan, step, requiredTaskFailed)
		if !step.Regression && step.Check.Required && ev.Outcome != verification.OutcomePassed {
			requiredTaskFailed = true
		}
		if err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
			latest, err := tx.LatestRunID(plan.submission)
			if err != nil {
				return err
			}
			if latest != plan.runID {
				return fault.New(fault.CodeVerificationRunning, "run %d was superseded by run %d while executing", plan.runID, latest)
			}
			_, err = tx.InsertEvidence(ev)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// runStep produces the evidence for one step: reused, skipped, or executed.
func (e *Engine) runStep(ctx context.Context, plan runPlan, step verification.Step, skipRegression bool) verification.Evidence {
	c := step.Check
	now := e.now()
	ev := verification.Evidence{
		RunID: plan.runID, CheckID: c.ID, CheckVersion: c.Version, CheckDigest: c.Digest(), Required: c.Required,
		Revision: plan.revision, PolicyDigest: plan.policy.Digest(), StartedAt: now, FinishedAt: now, ExitCode: -1,
	}
	if step.Regression && skipRegression {
		ev.Outcome = verification.OutcomeSkipped
		ev.Message = "skipped: a required task check failed"
		return ev
	}
	// Reuse: identical check content at the same immutable revision.
	var prior *verification.Evidence
	_ = e.store.Read(ctx, func(tx *sqlite.Tx) error {
		var err error
		prior, err = tx.FindReusableEvidence(c.Digest(), plan.revision)
		return err
	})
	if prior != nil && verification.Reusable(*prior, c, plan.revision) {
		ev.Outcome = verification.OutcomePassed
		ev.ExitCode = prior.ExitCode
		ev.Reused, ev.ReusedFrom = true, prior.ID
		ev.Message = fmt.Sprintf("reused evidence %d (run %d) for the same check at revision %s", prior.ID, prior.RunID, plan.revision)
		return ev
	}
	if plan.root == "" {
		ev.Outcome = verification.OutcomeError
		ev.Message = "project has no directory to run checks in; register one with `tasks init --path`"
		return ev
	}
	// The evidence must describe the submitted revision: refuse to run on
	// a tree that no longer matches it.
	if plan.revision != "" {
		st, err := workspace.Inspect(ctx, plan.root)
		switch {
		case err != nil:
			ev.Outcome = verification.OutcomeError
			ev.Message = err.Error()
			return ev
		case st.Revision != plan.revision:
			ev.Outcome = verification.OutcomeError
			ev.Message = fmt.Sprintf("working tree is at %s but the submission is revision %s; check out the submitted revision", st.Revision, plan.revision)
			return ev
		case !st.Clean():
			ev.Outcome = verification.OutcomeError
			ev.Message = fmt.Sprintf("working tree has %d uncommitted change(s); evidence would not describe revision %s", len(st.Dirty), plan.revision)
			return ev
		}
	}
	res := checkexec.Run(ctx, checkexec.Spec{Argv: c.Command, Dir: filepath.Join(plan.root, c.Dir), Timeout: c.EffectiveTimeout()})
	ev.StartedAt, ev.FinishedAt = res.StartedAt, res.FinishedAt
	ev.ExitCode, ev.Stdout, ev.Stderr = res.ExitCode, res.Stdout, res.Stderr
	switch {
	case !res.Started:
		ev.Outcome = verification.OutcomeError
		ev.Message = "could not start check: " + res.Err.Error()
	case res.TimedOut:
		ev.Outcome = verification.OutcomeTimeout
		ev.Message = res.Err.Error()
	case res.ExitCode == 0:
		ev.Outcome = verification.OutcomePassed
	default:
		ev.Outcome = verification.OutcomeFailed
		ev.Message = fmt.Sprintf("exit status %d", res.ExitCode)
		if res.Err != nil {
			ev.Message = res.Err.Error()
		}
	}
	return ev
}

// finishRun judges the evidence and records the verdict.
func (e *Engine) finishRun(ctx context.Context, plan runPlan) (VerifyResult, error) {
	now := e.now()
	var out VerifyResult
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		latest, err := tx.LatestRunID(plan.submission)
		if err != nil {
			return err
		}
		if latest != plan.runID {
			_ = tx.FinishRun(plan.runID, sqlite.RunError, fmt.Sprintf("superseded by run %d", latest), now)
			return fault.New(fault.CodeVerificationRunning, "run %d was superseded by run %d", plan.runID, latest)
		}
		evidence, err := tx.EvidenceForRun(plan.runID)
		if err != nil {
			return err
		}
		verdict := verification.Judge(plan.policy, evidence)
		status := sqlite.RunFailed
		if verdict.Passed {
			status = sqlite.RunPassed
		}
		if err := tx.FinishRun(plan.runID, status, verdict.Summary, now); err != nil {
			return err
		}
		if verdict.Passed && plan.proj.Integration == project.IntegrationNone {
			if err := tx.MarkComplete(plan.taskID, plan.submission, now); err != nil {
				return err
			}
		}
		r, err := tx.GetRecord(plan.taskID)
		if err != nil {
			return err
		}
		out = VerifyResult{TaskID: plan.taskID, SubmissionID: plan.submission, RunID: plan.runID, Revision: plan.revision, PolicyDigest: plan.policy.Digest(), Verification: status, Summary: verdict.Summary, Status: r.Status(now), Evidence: evidence}
		return nil
	})
	if err != nil {
		return VerifyResult{}, err
	}
	if out.Verification != sqlite.RunPassed {
		return out, &fault.Error{Code: fault.CodeVerificationFailed, Message: fmt.Sprintf("verification of task %s failed: %s; see `tasks evidence %s`", plan.taskID, out.Summary, plan.taskID), Details: out}
	}
	return out, nil
}

// RunView is a verification run with its evidence.
type RunView struct {
	ID           int64                   `json:"id"`
	SubmissionID int64                   `json:"submission_id"`
	Status       sqlite.RunStatus        `json:"status"`
	PolicyDigest string                  `json:"policy_digest"`
	Environment  string                  `json:"environment,omitempty"`
	CreatedAt    time.Time               `json:"created_at"`
	StartedAt    *time.Time              `json:"started_at,omitempty"`
	FinishedAt   *time.Time              `json:"finished_at,omitempty"`
	Summary      string                  `json:"summary,omitempty"`
	Evidence     []verification.Evidence `json:"evidence"`
}

// EvidencePage lists the runs (newest first) of a task's submissions.
type EvidencePage struct {
	TaskID      task.ID          `json:"task_id"`
	Submissions []SubmissionView `json:"submissions"`
	Runs        []RunView        `json:"runs"`
}

// Evidence returns every run and its evidence for a task. With runID > 0
// only that run is returned. Outputs are included; callers may truncate.
func (e *Engine) Evidence(ctx context.Context, id task.ID, runID int64) (EvidencePage, error) {
	page := EvidencePage{TaskID: id, Submissions: []SubmissionView{}, Runs: []RunView{}}
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		if _, err := tx.GetRecord(id); err != nil {
			return err
		}
		subs, err := tx.Submissions(id)
		if err != nil {
			return err
		}
		for i := range subs {
			page.Submissions = append(page.Submissions, *submissionView(&subs[i]))
			runs, err := tx.Runs(subs[i].ID)
			if err != nil {
				return err
			}
			for _, r := range runs {
				if runID > 0 && r.ID != runID {
					continue
				}
				ev, err := tx.EvidenceForRun(r.ID)
				if err != nil {
					return err
				}
				if ev == nil {
					ev = []verification.Evidence{}
				}
				page.Runs = append(page.Runs, RunView{ID: r.ID, SubmissionID: r.SubmissionID, Status: r.Status, PolicyDigest: r.PolicyDigest, Environment: r.Environment, CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Summary: r.Summary, Evidence: ev})
			}
		}
		// newest first
		for i, j := 0, len(page.Runs)-1; i < j; i, j = i+1, j-1 {
			page.Runs[i], page.Runs[j] = page.Runs[j], page.Runs[i]
		}
		if runID > 0 && len(page.Runs) == 0 {
			return fault.New(fault.CodeNotFound, "task %s has no verification run %d", id, runID)
		}
		return nil
	})
	return page, err
}
