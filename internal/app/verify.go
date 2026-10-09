package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/checkexec"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

func environmentFingerprint() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s/%s %s host=%s", runtime.GOOS, runtime.GOARCH, runtime.Version(), host)
}

// Verify runs checks under a session's authority.
//
//   - ModeTask / ModeRegression run that category fresh in the attempt's
//     workspace (editable, so results are diagnostic) and never complete,
//     release, or integrate.
//   - ModeComplete records an immutable submission (a clean commit in Git
//     projects), runs BOTH categories fresh on a detached snapshot of that
//     revision, integrates when the project requires it, and then, in one
//     short transaction that re-checks authority, prerequisites, contract
//     and policy, records completion and ends the claim. For cohort
//     members it records the submission, ends the claim, and runs the
//     cohort job if every peer has submitted.
//
// Every invocation executes checks; stored evidence is never reused as
// proof. A failing result is returned as VERIFICATION_FAILED with the
// result in Details; the claim stays live so the agent can repair.
func (e *Engine) Verify(ctx context.Context, token execution.Token, mode verification.Mode) (VerifyResult, error) {
	if _, err := verification.ParseMode(string(mode)); err != nil {
		return VerifyResult{}, err
	}
	if mode == verification.ModeComplete {
		return e.verifyComplete(ctx, token)
	}
	return e.verifyDiagnostic(ctx, token, mode)
}

// prepared is what the preparation read gathers before any Git call.
type prepared struct {
	attempt execution.Attempt
	rec     sqlite.Record
	proj    project.Project
	policy  verification.Policy
	dir     string
}

func (e *Engine) prepare(ctx context.Context, token execution.Token) (prepared, error) {
	now := e.now()
	var p prepared
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		a, err := e.authorize(tx, token, now)
		if err != nil {
			return err
		}
		p.attempt = a
		if p.rec, err = tx.GetRecord(a.TaskID); err != nil {
			return err
		}
		if p.proj, err = tx.GetProject(p.rec.Task.ProjectID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return p, err
	}
	p.policy = effectivePolicy(p.rec.Task, p.proj)
	p.dir = p.attempt.WorkspacePath
	if p.dir == "" {
		return p, fault.New(fault.CodeWorkspaceUnavailable, "attempt has no worktree; claim the task again")
	}
	return p, nil
}

func (e *Engine) verifyDiagnostic(ctx context.Context, token execution.Token, mode verification.Mode) (VerifyResult, error) {
	p, err := e.prepare(ctx, token)
	if err != nil {
		return VerifyResult{}, err
	}
	policy := p.policy.Subset(mode)
	gate := policy.TaskChecks
	if mode == verification.ModeRegression {
		gate = policy.Regression
	}
	if err := verification.RequireGate(gate, string(mode)+" checks"); err != nil {
		return VerifyResult{}, fault.New(fault.CodeMissingVerification, "%s: %v; `verify complete` will fail closed", p.rec.Task.ID, fault.MessageOf(err))
	}
	revision, dirty := "", false
	if st, err := workspace.Inspect(ctx, p.dir); err == nil {
		revision, dirty = st.Revision, !st.Clean()
	}
	now := e.now()
	var runID int64
	err = e.store.Write(ctx, func(tx *sqlite.Tx) error {
		if _, err := e.touch(tx, token, now); err != nil {
			return err
		}
		var err error
		runID, err = tx.InsertRun(sqlite.NewRun{TaskID: p.rec.Task.ID, AttemptID: p.attempt.ID, Mode: mode, Status: sqlite.RunRunning, Revision: revision, Policy: policy, Environment: environmentFingerprint()}, now)
		return err
	})
	if err != nil {
		return VerifyResult{}, err
	}
	stop := e.heartbeat(ctx, token)
	fence := func(tx *sqlite.Tx) error { _, err := e.authorize(tx, token, e.now()); return err }
	evidence, err := e.runChecks(ctx, p.dir, policy, runID, revision, fence)
	stop()
	res := VerifyResult{TaskID: p.rec.Task.ID, Mode: mode, RunID: runID, Revision: revision, Evidence: evidence}
	if err != nil {
		_ = e.store.Write(ctx, func(tx *sqlite.Tx) error { return tx.FinishRun(runID, sqlite.RunError, err.Error(), e.now()) })
		return res, err
	}
	verdict := verification.Judge(policy, evidence)
	status := sqlite.RunFailed
	if verdict.Passed {
		status = sqlite.RunPassed
	}
	summary := verdict.Summary
	if dirty {
		summary += "; workspace had uncommitted changes (diagnostic only)"
	}
	if err := e.store.Write(ctx, func(tx *sqlite.Tx) error { return tx.FinishRun(runID, status, summary, e.now()) }); err != nil {
		return res, err
	}
	res.Passed, res.Summary = verdict.Passed, summary
	res.Status = p.rec.Status(e.now(), p.proj.MaxAttempts)
	res.Message = fmt.Sprintf("%s checks %s; this run does not complete the task", mode, status)
	if !verdict.Passed {
		return res, &fault.Error{Code: fault.CodeVerificationFailed, Message: fmt.Sprintf("%s checks failed for %s: %s", mode, p.rec.Task.ID, summary), Details: res}
	}
	return res, nil
}

// runChecks executes a policy's checks in dir in planner order, committing
// each evidence row as it lands. fence is evaluated in each evidence
// transaction; a failure (lost authority, superseded run) stops the run.
func (e *Engine) runChecks(ctx context.Context, dir string, policy verification.Policy, runID int64, revision string, fence func(*sqlite.Tx) error) ([]verification.Evidence, error) {
	var out []verification.Evidence
	requiredTaskFailed := false
	for _, step := range verification.Plan(policy) {
		if err := ctx.Err(); err != nil {
			return out, fault.Wrap(err, fault.CodeInternal, "verification interrupted")
		}
		ev := e.execStep(ctx, dir, step, runID, revision, policy.Digest(), requiredTaskFailed)
		if !step.Regression && step.Check.Required && ev.Outcome != verification.OutcomePassed {
			requiredTaskFailed = true
		}
		if err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
			if err := fence(tx); err != nil {
				return err
			}
			id, err := tx.InsertEvidence(ev)
			ev.ID = id
			return err
		}); err != nil {
			return out, err
		}
		out = append(out, ev)
	}
	return out, nil
}

func (e *Engine) execStep(ctx context.Context, dir string, step verification.Step, runID int64, revision, policyDigest string, skipRegression bool) verification.Evidence {
	c := step.Check
	now := e.now()
	ev := verification.Evidence{RunID: runID, CheckID: c.ID, CheckVersion: c.Version, CheckDigest: c.Digest(), Required: c.Required, Revision: revision, PolicyDigest: policyDigest, StartedAt: now, FinishedAt: now, ExitCode: -1}
	if step.Regression && skipRegression {
		ev.Outcome = verification.OutcomeSkipped
		ev.Message = "skipped: a required task check failed"
		return ev
	}
	res := checkexec.Run(ctx, checkexec.Spec{Argv: c.Command, Dir: filepath.Join(dir, c.Dir), Timeout: c.EffectiveTimeout(), Env: checkEnv(dir)})
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

// checkEnv is the environment checks run with: the parent environment
// minus any session token, plus GOFLAGS=-count=1 so Go's own test cache
// cannot turn a fresh run into a replay.
func checkEnv(dir string) []string {
	var env []string
	flags := "-count=1"
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "AT_SESSION="), strings.HasPrefix(kv, "TASKS_SESSION="):
			continue
		case strings.HasPrefix(kv, "GOFLAGS="):
			flags = strings.TrimPrefix(kv, "GOFLAGS=") + " -count=1"
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GOFLAGS="+flags, "AT_CHECK_DIR="+dir)
}

// finalRun carries the state of one verify-complete invocation.
type finalRun struct {
	prepared
	token        execution.Token
	revision     string
	submission   int64
	runID        int64
	contractRev  int
	regressionID string
}

func (e *Engine) verifyComplete(ctx context.Context, token execution.Token) (VerifyResult, error) {
	// Idempotent acknowledgement of an already-committed completion.
	if res, ok, err := e.replayCompletion(ctx, token); err != nil || ok {
		return res, err
	}
	p, err := e.prepare(ctx, token)
	if err != nil {
		return VerifyResult{}, err
	}
	if p.rec.Task.Kind != task.KindTask {
		return VerifyResult{}, fault.New(fault.CodeInvalidInput, "%s is a group", p.rec.Task.ID)
	}
	if missing := p.policy.MissingCategory(); missing != "" {
		return VerifyResult{}, fault.New(fault.CodeMissingVerification, "%s cannot complete: no %s are defined (completion fails closed)", p.rec.Task.ID, missing)
	}
	if p.rec.UnmetRequires > 0 {
		return VerifyResult{}, fault.New(fault.CodeTaskBlocked, "%s cannot complete: %d prerequisite(s) are not complete", p.rec.Task.ID, p.rec.UnmetRequires)
	}
	st, err := workspace.RequireClean(ctx, p.dir)
	if err != nil {
		return VerifyResult{}, err
	}
	revision := st.Revision
	fr := finalRun{prepared: p, token: token, revision: revision, contractRev: p.rec.Task.ContractRev, regressionID: (verification.Policy{Regression: p.proj.Regression}).Digest()}
	if p.rec.Task.Cohort != "" {
		return e.submitForCohort(ctx, fr)
	}
	now := e.now()
	err = e.store.Write(ctx, func(tx *sqlite.Tx) error {
		if _, err := e.touch(tx, token, now); err != nil {
			return err
		}
		r, err := tx.GetRecord(p.rec.Task.ID)
		if err != nil {
			return err
		}
		if r.UnmetRequires > 0 {
			return fault.New(fault.CodeTaskBlocked, "%s cannot complete: prerequisites changed", r.Task.ID)
		}
		// One final run per attempt at a time: a double-fired `verify
		// complete` must neither race its twin nor count a failure.
		if s := r.Submission; s != nil && s.AttemptID == p.attempt.ID && s.RunStatus() == sqlite.RunRunning {
			return fault.New(fault.CodeVerificationRunning, "%s: a `verify complete` run (%d) is already in progress for this attempt; wait for its result", r.Task.ID, s.Run.ID)
		}
		if fr.submission, err = tx.InsertSubmission(r.Task.ID, p.attempt.ID, revision, "", now); err != nil {
			return err
		}
		fr.runID, err = tx.InsertRun(sqlite.NewRun{TaskID: r.Task.ID, AttemptID: p.attempt.ID, SubmissionID: fr.submission, Mode: verification.ModeComplete, Status: sqlite.RunRunning, Revision: revision, Policy: p.policy, Environment: environmentFingerprint()}, now)
		return err
	})
	if err != nil {
		return VerifyResult{}, err
	}
	e.notify()
	stop := e.heartbeat(ctx, token)
	defer stop()
	res, err := e.executeFinal(ctx, fr)
	e.notify()
	return res, err
}

// executeFinal runs both suites on an immutable snapshot, integrates, and
// finalises. It is used by the single-task path; cohorts have their own.
func (e *Engine) executeFinal(ctx context.Context, fr finalRun) (VerifyResult, error) {
	res := VerifyResult{TaskID: fr.rec.Task.ID, Mode: verification.ModeComplete, RunID: fr.runID, Revision: fr.revision, SubmissionID: fr.submission}
	fence := func(tx *sqlite.Tx) error {
		if _, err := e.authorize(tx, fr.token, e.now()); err != nil {
			return err
		}
		latest, err := tx.LatestRunID(fr.submission)
		if err != nil {
			return err
		}
		if latest != fr.runID {
			return fault.New(fault.CodeSessionSuperseded, "run %d superseded", fr.runID)
		}
		return nil
	}
	// fail records why the run did not complete. Only a genuine failed
	// proof counts against the attempt (once per attempt, fenced to the
	// current generation); environment and authority problems are kept
	// as last_error so the next attempt and humans can see them.
	fail := func(status sqlite.RunStatus, code fault.Code, summary string, evidence []verification.Evidence) (VerifyResult, error) {
		now := e.now()
		class := classify(code)
		_ = e.store.Write(ctx, func(tx *sqlite.Tx) error {
			if err := tx.FinishRun(fr.runID, status, summary, now); err != nil {
				return err
			}
			if class == classAttempt {
				_, err := tx.RecordFailure(fr.rec.Task.ID, fr.attempt.ID, fr.proj.RetryCooldown, now)
				return err
			}
			return tx.SetLastError(fr.rec.Task.ID, fr.attempt.ID, string(code)+": "+summary, now)
		})
		res.Evidence, res.Summary = evidence, summary
		res.Status = e.statusOf(ctx, fr.rec.Task.ID)
		switch class {
		case classAttempt:
			res.Message = "task NOT complete: a required check failed; the claim stays live for repair (counted against the attempt budget once)"
		case classEnvironment:
			res.Message = "task NOT complete: the environment stopped the run, not the checks; fix the cause and verify again (not counted as a failure)"
		default:
			res.Message = "task NOT complete: authority or contract changed during verification; verify again under the current contract (not counted as a failure)"
		}
		return res, &fault.Error{Code: code, Message: fmt.Sprintf("%s: %s", fr.rec.Task.ID, summary), Details: res}
	}
	// Immutable inputs: a detached snapshot of the submitted revision.
	mgr := e.manager(fr.proj)
	dir, cleanup, err := mgr.Snapshot(ctx, fr.revision)
	if err != nil {
		return fail(sqlite.RunError, fault.CodeWorkspaceUnavailable, err.Error(), nil)
	}
	evidence, err := e.runChecks(ctx, dir, fr.policy, fr.runID, fr.revision, fence)
	cleanup()
	if err != nil {
		res.Evidence = evidence
		_ = e.store.Write(ctx, func(tx *sqlite.Tx) error { return tx.FinishRun(fr.runID, sqlite.RunError, err.Error(), e.now()) })
		return res, err
	}
	verdict := verification.Judge(fr.policy, evidence)
	if !verdict.Passed {
		return fail(sqlite.RunFailed, fault.CodeVerificationFailed, verdict.Summary, evidence)
	}
	spec := intentSpec{proj: fr.proj, taskID: fr.rec.Task.ID, attemptID: fr.attempt.ID, runID: fr.runID, submission: fr.submission, owner: fr.token.Digest(), contractRev: fr.contractRev, regression: fr.regressionID, policy: fr.policy, source: fr.revision}
	done := func(final string) (VerifyResult, error) {
		res.Evidence, res.Passed, res.Completed = evidence, true, true
		res.IntegratedRevision = final
		if final == fr.revision {
			res.IntegratedRevision = ""
		}
		res.Status = task.StatusComplete
		res.Summary = verdict.Summary
		res.Message = "task complete; claim ended"
		return res, nil
	}
	// refused maps a finalisation refusal (changed contract, lost
	// authority, ...) to the caller's error; the target never moved.
	refused := func(err error) (VerifyResult, error) {
		code := fault.CodeOf(err)
		if code == fault.CodeInternal {
			return res, err
		}
		return fail(sqlite.RunFailed, code, err.Error(), evidence)
	}
	if err := e.fault("final:after-checks"); err != nil {
		return res, err
	}
	if fr.proj.Integration != project.IntegrationPromote {
		// No promotion: one transaction re-checks everything completion
		// depends on and records it.
		now := e.now()
		err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
			if err := fence(tx); err != nil {
				return err
			}
			if _, err := e.recheckForCompletion(tx, spec, fr.revision); err != nil {
				return err
			}
			if err := tx.FinishRun(fr.runID, sqlite.RunPassed, verdict.Summary, now); err != nil {
				return err
			}
			if err := tx.MarkComplete(fr.rec.Task.ID, fr.submission, now); err != nil {
				return err
			}
			return tx.EndAttempt(fr.attempt.ID, execution.EndFinished, now)
		})
		if err != nil {
			return refused(err)
		}
		return done(fr.revision)
	}
	// Guarded, recoverable promotion (see integrate.go): build the
	// candidate on the current target, verify it when the merge changed
	// content, pin an intent, compare-and-swap the target, then record
	// completion. A moved target rebuilds (bounded); a crash anywhere is
	// resolved by reconciliation from the intent and Git.
	for attempt := 0; attempt < 3; attempt++ {
		cand, err := mgr.PrepareMerge(ctx, fr.proj.TargetBranch, []string{fr.revision}, fmt.Sprintf("at: integrate %s (%s)", fr.rec.Task.ID, fr.rec.Task.Description))
		if err != nil {
			return fail(sqlite.RunFailed, fault.CodeIntegrationFailed, err.Error(), evidence)
		}
		final := fr.revision
		if cand.Revision != fr.revision {
			snap, snapCleanup, err := mgr.Snapshot(ctx, cand.Revision)
			if err != nil {
				cand.Cleanup()
				return fail(sqlite.RunError, fault.CodeWorkspaceUnavailable, err.Error(), evidence)
			}
			more, err := e.runChecks(ctx, snap, fr.policy, fr.runID, cand.Revision, fence)
			snapCleanup()
			if err != nil {
				cand.Cleanup()
				_ = e.store.Write(ctx, func(tx *sqlite.Tx) error { return tx.FinishRun(fr.runID, sqlite.RunError, err.Error(), e.now()) })
				return res, err
			}
			evidence = append(evidence, more...)
			if v := verification.Judge(fr.policy, more); !v.Passed {
				cand.Cleanup()
				return fail(sqlite.RunFailed, fault.CodeVerificationFailed, "integrated candidate "+short(cand.Revision)+": "+v.Summary, evidence)
			}
			final = cand.Revision
		}
		intentID, err := e.recordIntent(ctx, spec, cand, final, verdict.Summary, fence)
		if err != nil {
			cand.Cleanup()
			return refused(err)
		}
		if err := e.fault("final:after-intent"); err != nil {
			cand.Cleanup()
			return res, err
		}
		err = mgr.Promote(ctx, fr.proj.TargetBranch, cand)
		cand.Cleanup()
		switch {
		case err == nil:
		case errors.Is(err, workspace.ErrTargetMoved):
			e.abandonIntent(ctx, intentID, "target moved before promotion; rebuilding the candidate")
			continue
		default:
			e.abandonIntent(ctx, intentID, "promotion failed: "+err.Error())
			return fail(sqlite.RunFailed, fault.CodeIntegrationFailed, err.Error(), evidence)
		}
		if err := e.fault("final:after-promote"); err != nil {
			return res, err
		}
		// The target carries the candidate: record completion for the intent.
		// Authority is the intent (owned by this token), not the lease.
		err = e.store.Write(ctx, func(tx *sqlite.Tx) error {
			in, err := tx.GetIntent(intentID)
			if err != nil {
				return err
			}
			return e.completeIntentTx(tx, in, spec.owner, verdict.Summary)
		})
		if err != nil {
			// The intent stays open; reconciliation completes it from Git.
			return res, fault.Wrap(err, fault.CodeInternal, "record completion of promoted %s (will be reconciled)", fr.rec.Task.ID)
		}
		e.notify()
		if err := e.fault("final:after-complete"); err != nil {
			return res, err
		}
		return done(final)
	}
	return fail(sqlite.RunFailed, fault.CodeIntegrationFailed, "target branch kept moving; verify again", evidence)
}

// replayCompletion returns the committed result when the token's attempt
// already completed its task, so a client that crashed after the commit
// gets an acknowledgement without a new run or new authority.
func (e *Engine) replayCompletion(ctx context.Context, token execution.Token) (VerifyResult, bool, error) {
	var res VerifyResult
	ok := false
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		auth, err := tx.AttemptByDigest(token.Digest())
		if err != nil {
			return err
		}
		a := auth.Attempt
		if a.EndedAt == nil || a.EndReason != execution.EndFinished {
			return nil
		}
		r, err := tx.GetRecord(a.TaskID)
		if err != nil {
			return err
		}
		if r.CompletedAt == nil || r.CompletedSubmissionID == 0 {
			return nil
		}
		// The acknowledgement is the run that established completion, which
		// is not necessarily the newest submission (a double-fired call may
		// have recorded a later, refused one).
		subs, err := tx.Submissions(a.TaskID)
		if err != nil {
			return err
		}
		for _, s := range subs {
			if s.ID != r.CompletedSubmissionID || s.AttemptID != a.ID || s.Run == nil {
				continue
			}
			ev, err := tx.EvidenceForRun(s.Run.ID)
			if err != nil {
				return err
			}
			res = VerifyResult{TaskID: a.TaskID, Mode: verification.ModeComplete, RunID: s.Run.ID, Revision: s.Revision, IntegratedRevision: s.Run.IntegratedRevision, SubmissionID: s.ID, Passed: true, Completed: true, Status: task.StatusComplete, Summary: s.Run.Summary, Evidence: ev, Replayed: true, Message: "already complete; stored result returned, no checks re-run"}
			ok = true
		}
		return nil
	})
	return res, ok, err
}

func (e *Engine) statusOf(ctx context.Context, id task.ID) task.Status {
	st := task.StatusReady
	_ = e.store.Read(ctx, func(tx *sqlite.Tx) error {
		r, err := tx.GetRecord(id)
		if err != nil {
			return err
		}
		p, err := tx.GetProject(r.Task.ProjectID)
		if err != nil {
			return err
		}
		st = r.Status(e.now(), p.MaxAttempts)
		return nil
	})
	return st
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

var _ = time.Second
