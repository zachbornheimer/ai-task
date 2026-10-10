package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	opCtx, stop := e.heartbeat(ctx, token)
	fence := func(tx *sqlite.Tx) error { _, err := e.authorize(tx, token, e.now()); return err }
	changed, _ := workspace.ChangedFiles(ctx, p.dir, "refs/heads/"+p.proj.TargetBranch)
	if st, err := workspace.Inspect(ctx, p.dir); err == nil {
		for _, line := range st.Dirty {
			if len(line) > 3 {
				changed = append(changed, line[3:])
			}
		}
	}
	taskOpts, regOpts := suiteOpts(p.rec.Task, p.proj, changed)
	opts := taskOpts
	if mode == verification.ModeRegression {
		opts = regOpts
	}
	evidence, err := e.runChecks(opCtx, p.dir, policy, runID, revision, fence, opts)
	if terr := stop(); terr != nil && err != nil {
		err = fault.Wrap(terr, fault.CodeOf(terr), "verification aborted: claim authority was lost while checks ran")
	}
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
		summary += "; ran in the working tree with uncommitted changes (diagnostic only: `verify complete` tests the committed revision)"
	}
	if err := e.store.Write(ctx, func(tx *sqlite.Tx) error { return tx.FinishRun(runID, status, summary, e.now()) }); err != nil {
		return res, err
	}
	res.Passed, res.Summary = verdict.Passed, summary
	res.Status = p.rec.Status(e.now(), p.proj.MaxAttempts)
	res.Message = fmt.Sprintf("%s checks %s; this run does not complete the task", mode, status)
	res.Warnings = budgetWarnings(evidence, policy, taskOpts.budget, regOpts.budget)
	if len(verdict.TimedOut) > 0 {
		res.Message = timeoutMessage(verdict, policy, taskOpts.budget, regOpts.budget, p.rec.Task.Size)
		return res, &fault.Error{Code: fault.CodeVerificationTimeout, Message: fmt.Sprintf("%s checks timed out for %s: %s", mode, p.rec.Task.ID, summary), Details: res}
	}
	if !verdict.Passed {
		return res, &fault.Error{Code: fault.CodeVerificationFailed, Message: fmt.Sprintf("%s checks failed for %s: %s", mode, p.rec.Task.ID, summary), Details: res}
	}
	return res, nil
}

// runSuites runs a policy's two categories on separate, disposable
// snapshots of one revision: task checks first, then (unless a required
// task check failed, in which case the regression rows are recorded as
// skipped) the regression checks on a fresh snapshot. Nothing a task check
// leaves behind can be the reason a regression check passes.
func (e *Engine) runSuites(ctx context.Context, mgr workspace.Manager, revision string, policy verification.Policy, runID int64, fence func(*sqlite.Tx) error, t task.Task, proj project.Project) ([]verification.Evidence, error) {
	var out []verification.Evidence
	changed, _ := workspace.ChangedBetween(ctx, mgr.Repo, "refs/heads/"+proj.TargetBranch, revision)
	taskOpts, regOpts := suiteOpts(t, proj, changed)
	taskPolicy := verification.Policy{TaskChecks: policy.TaskChecks}
	if len(taskPolicy.TaskChecks) > 0 {
		dir, cleanup, err := mgr.Snapshot(ctx, revision)
		if err != nil {
			return nil, err
		}
		ev, err := e.runChecks(ctx, dir, taskPolicy, runID, revision, fence, taskOpts)
		cleanup()
		out = append(out, ev...)
		if err != nil {
			return out, err
		}
	}
	if len(policy.Regression) == 0 {
		return out, nil
	}
	requiredTaskFailed := false
	for _, ev := range out {
		if ev.Required && ev.Outcome != verification.OutcomePassed {
			requiredTaskFailed = true
		}
	}
	regPolicy := verification.Policy{Regression: policy.Regression}
	if requiredTaskFailed {
		ev, err := e.runChecksSkipping(ctx, regPolicy, runID, revision, policy.Digest(), fence)
		return append(out, ev...), err
	}
	dir, cleanup, err := mgr.Snapshot(ctx, revision)
	if err != nil {
		return out, err
	}
	ev, err := e.runChecks(ctx, dir, regPolicy, runID, revision, fence, regOpts)
	cleanup()
	return append(out, ev...), err
}

// runChecksSkipping records every check of policy as skipped (a required
// task check failed earlier in the run).
func (e *Engine) runChecksSkipping(ctx context.Context, policy verification.Policy, runID int64, revision, digest string, fence func(*sqlite.Tx) error) ([]verification.Evidence, error) {
	var out []verification.Evidence
	for _, step := range verification.Plan(policy) {
		ev := e.execStep(ctx, "", step, runID, revision, digest, true, 0, nil)
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

// runOpts tunes one suite run: the time budget for the whole suite (0 =
// none), what the budget is called in messages, and the files the
// candidate changed against the target, handed to checks as
// AT_CHANGED_FILES so a regression check can target affected tests.
type runOpts struct {
	budget     time.Duration
	budgetName string
	changed    []string
	target     string
}

// suiteOpts derives the task-check and regression budgets for a task
// from its size and the project.
func suiteOpts(t task.Task, proj project.Project, changed []string) (taskOpts, regOpts runOpts) {
	size := t.Size
	if size == "" {
		size = task.SizeSmall
	}
	taskOpts = runOpts{budget: proj.Budgets.ForSize(size), budgetName: fmt.Sprintf("the %s task-check budget", size), changed: changed, target: proj.TargetBranch}
	regOpts = runOpts{budget: proj.Budgets.RegressionBudget(), budgetName: "the regression budget", changed: changed, target: proj.TargetBranch}
	return taskOpts, regOpts
}

// runChecks executes a policy's checks in dir in planner order, committing
// each evidence row as it lands. fence is evaluated in each evidence
// transaction; a failure (lost authority, superseded run) stops the run.
// The suite shares one budget: a check that would start after it is spent
// is recorded as timed out without running.
func (e *Engine) runChecks(ctx context.Context, dir string, policy verification.Policy, runID int64, revision string, fence func(*sqlite.Tx) error, opts runOpts) ([]verification.Evidence, error) {
	var out []verification.Evidence
	requiredTaskFailed := false
	var deadline time.Time
	if opts.budget > 0 {
		deadline = time.Now().Add(opts.budget)
	}
	extra := changedFilesEnv(dir, opts)
	for _, step := range verification.Plan(policy) {
		if err := ctx.Err(); err != nil {
			return out, fault.Wrap(err, fault.CodeInternal, "verification interrupted")
		}
		limit := step.Check.EffectiveTimeout()
		budgetLimited := false
		if !deadline.IsZero() {
			if left := time.Until(deadline); left < limit {
				limit, budgetLimited = left, true
			}
		}
		ev := e.execStep(ctx, dir, step, runID, revision, policy.Digest(), requiredTaskFailed, limit, extra)
		if ev.Outcome == verification.OutcomeTimeout && budgetLimited {
			ev.Message = fmt.Sprintf("exceeded %s (%s for the whole suite); the check is too slow for the task's size, or the code it runs hangs", opts.budgetName, opts.budget)
		} else if ev.Outcome == verification.OutcomeTimeout {
			ev.Message = fmt.Sprintf("exceeded the check's own timeout (%s)", limit)
		}
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

func (e *Engine) execStep(ctx context.Context, dir string, step verification.Step, runID int64, revision, policyDigest string, skipRegression bool, timeout time.Duration, extraEnv []string) verification.Evidence {
	c := step.Check
	now := e.now()
	ev := verification.Evidence{RunID: runID, CheckID: c.ID, CheckVersion: c.Version, CheckDigest: c.Digest(), Required: c.Required, Revision: revision, PolicyDigest: policyDigest, StartedAt: now, FinishedAt: now, ExitCode: -1}
	if step.Regression && skipRegression {
		ev.Outcome = verification.OutcomeSkipped
		ev.Message = "skipped: a required task check failed"
		return ev
	}
	if timeout <= 0 && extraEnv != nil {
		// The suite's budget is spent: nothing left for this check.
		ev.Outcome = verification.OutcomeTimeout
		ev.Message = "not run: the suite's budget was spent by earlier checks"
		return ev
	}
	if timeout <= 0 {
		timeout = c.EffectiveTimeout()
	}
	res := checkexec.Run(ctx, checkexec.Spec{Argv: c.Command, Dir: filepath.Join(dir, c.Dir), Timeout: timeout, Env: append(checkEnv(dir), extraEnv...)})
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
// cannot turn a fresh run into a replay, and a TMPDIR private to the
// snapshot so temporary files of one suite never reach another.
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
		case strings.HasPrefix(kv, "TMPDIR="):
			continue
		}
		env = append(env, kv)
	}
	tmp := dir + ".tmp"
	if dir == "" {
		tmp = os.TempDir()
	} else {
		_ = os.MkdirAll(tmp, 0o700)
	}
	return append(env, "GOFLAGS="+flags, "AT_CHECK_DIR="+dir, "TMPDIR="+tmp)
}

// changedFilesEnv writes the candidate's changed paths next to the
// snapshot and names the file and the target branch for the checks.
// Always non-nil so execStep can tell a spent budget from "no budget".
func changedFilesEnv(dir string, opts runOpts) []string {
	env := []string{"AT_TARGET_BRANCH=" + opts.target}
	tmp := os.TempDir()
	if dir != "" {
		tmp = dir + ".tmp"
		_ = os.MkdirAll(tmp, 0o700)
	}
	path := filepath.Join(tmp, "at-changed-files")
	if err := os.WriteFile(path, []byte(strings.Join(opts.changed, "\n")+"\n"), 0o600); err == nil {
		env = append(env, "AT_CHANGED_FILES="+path)
	}
	return env
}

// budgetWarnings names required checks that passed but used more than
// half of their suite's budget: CI machines are slower, so this is the
// moment to speed the check up, split the task, or declare a larger size.
func budgetWarnings(evidence []verification.Evidence, policy verification.Policy, taskBudget, regBudget time.Duration) []string {
	regression := map[string]bool{}
	for _, c := range policy.Regression {
		regression[c.ID] = true
	}
	var w []string
	for _, ev := range evidence {
		if !ev.Required || ev.Outcome != verification.OutcomePassed {
			continue
		}
		budget := taskBudget
		if regression[ev.CheckID] {
			budget = regBudget
		}
		if budget <= 0 {
			continue
		}
		if took := ev.FinishedAt.Sub(ev.StartedAt); took*2 > budget {
			w = append(w, fmt.Sprintf("check %s took %s of its %s budget; make it faster, split the task, or raise the size (CI is slower than this machine)", ev.CheckID, took.Round(time.Millisecond), budget))
		}
	}
	return w
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
	// checkCtx is cancelled by the heartbeat when authority is lost, so
	// check processes never outlive the claim.
	checkCtx context.Context
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
	opCtx, stop := e.heartbeat(ctx, token)
	fr.checkCtx = opCtx
	res, err := e.executeFinal(ctx, fr)
	if terr := stop(); terr != nil && err != nil && opCtx.Err() != nil && ctx.Err() == nil {
		err = fault.Wrap(terr, fault.CodeOf(terr), "verification aborted: claim authority was lost while checks ran")
	}
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
		switch {
		case code == fault.CodeVerificationTimeout:
			// message set by the caller: which budget, and whose problem
		case class == classAttempt:
			res.Message = "task NOT complete: a required check failed; the claim stays live for repair (counted against the attempt budget once)"
		case code == fault.CodeIntegrationFailed && len(res.Conflicts) > 0:
			res.Message = fmt.Sprintf("task NOT complete: the submitted revision conflicts with %s (%s); in the worktree run %s, resolve the conflicts, commit, and `at verify complete` again (not counted as a failure)", fr.proj.TargetBranch, strings.Join(res.Conflicts, ", "), integrateStep(fr.proj.TargetBranch))
		case code == fault.CodeIntegrationFailed:
			res.Message = fmt.Sprintf("task NOT complete: the submitted revision could not be promoted to %s; in the worktree run %s, commit, and `at verify complete` again (not counted as a failure)", fr.proj.TargetBranch, integrateStep(fr.proj.TargetBranch))
		case class == classEnvironment:
			res.Message = "task NOT complete: the environment stopped the run, not the checks; fix the cause and verify again (not counted as a failure)"
		default:
			res.Message = "task NOT complete: authority or contract changed during verification; verify again under the current contract (not counted as a failure)"
		}
		return res, &fault.Error{Code: code, Message: fmt.Sprintf("%s: %s", fr.rec.Task.ID, summary), Details: res}
	}
	// conflict records the conflicting files of a merge failure on the
	// result before fail renders it.
	conflict := func(err error) (VerifyResult, error) {
		var ce *workspace.ConflictError
		if errors.As(err, &ce) {
			res.Conflicts = ce.Files
		}
		return fail(sqlite.RunFailed, fault.CodeIntegrationFailed, fault.MessageOf(err), res.Evidence)
	}
	mgr := e.manager(fr.proj)
	checkCtx := fr.checkCtx
	if checkCtx == nil {
		checkCtx = ctx
	}
	// Pinned paths are part of the contract: a submission that changed one
	// fails as the attempt's own failure, before any check runs.
	if len(fr.rec.Task.Pins) > 0 {
		changed, err := workspace.ChangedBetween(ctx, fr.proj.RootPath, "refs/heads/"+fr.proj.TargetBranch, fr.revision)
		if err != nil {
			return fail(sqlite.RunError, fault.CodeWorkspaceUnavailable, "list changed files: "+fault.MessageOf(err), nil)
		}
		if hit := pinnedChanges(fr.rec.Task.Pins, changed); len(hit) > 0 {
			return fail(sqlite.RunFailed, fault.CodeVerificationFailed, "pinned path(s) changed by this task: "+strings.Join(hit, ", ")+"; the task may not modify them (revert the change, or ask the planner to unpin)", nil)
		}
	}
	integrate := fmt.Sprintf("at: integrate %s (%s)", fr.rec.Task.ID, fr.rec.Task.Description)
	// A submission that cannot merge into the target is refused before any
	// check runs: the holder has a merge to resolve, and the checks would
	// only be repeated on the merged result. The candidate built here is
	// reused by the first promotion attempt below.
	var prepared *workspace.Candidate
	if fr.proj.Integration == project.IntegrationPromote {
		cand, err := mgr.PrepareMerge(ctx, fr.proj.TargetBranch, []string{fr.revision}, integrate)
		if err != nil {
			return conflict(err)
		}
		prepared = &cand
		defer func() {
			if prepared != nil {
				prepared.Cleanup()
			}
		}()
	}
	// Immutable inputs: detached snapshots of the submitted revision, one
	// per check category.
	evidence, err := e.runSuites(checkCtx, mgr, fr.revision, fr.policy, fr.runID, fence, fr.rec.Task, fr.proj)
	if err != nil && fault.CodeOf(err) == fault.CodeWorkspaceUnavailable && len(evidence) == 0 {
		return fail(sqlite.RunError, fault.CodeWorkspaceUnavailable, err.Error(), nil)
	}
	if err != nil {
		res.Evidence = evidence
		_ = e.store.Write(ctx, func(tx *sqlite.Tx) error { return tx.FinishRun(fr.runID, sqlite.RunError, err.Error(), e.now()) })
		return res, err
	}
	verdict := verification.Judge(fr.policy, evidence)
	taskBudget, regBudget := fr.proj.Budgets.ForSize(fr.rec.Task.Size), fr.proj.Budgets.RegressionBudget()
	if len(verdict.TimedOut) > 0 {
		res.Message = timeoutMessage(verdict, fr.policy, taskBudget, regBudget, fr.rec.Task.Size)
		return fail(sqlite.RunFailed, fault.CodeVerificationTimeout, verdict.Summary, evidence)
	}
	if !verdict.Passed {
		return fail(sqlite.RunFailed, fault.CodeVerificationFailed, verdict.Summary, evidence)
	}
	res.Warnings = budgetWarnings(evidence, fr.policy, taskBudget, regBudget)
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
		var cand workspace.Candidate
		if prepared != nil {
			cand, prepared = *prepared, nil
		} else {
			var err error
			cand, err = mgr.PrepareMerge(ctx, fr.proj.TargetBranch, []string{fr.revision}, integrate)
			if err != nil {
				res.Evidence = evidence
				return conflict(err)
			}
		}
		final := fr.revision
		if cand.Revision != fr.revision {
			more, err := e.runSuites(checkCtx, mgr, cand.Revision, fr.policy, fr.runID, fence, fr.rec.Task, fr.proj)
			if err != nil && fault.CodeOf(err) == fault.CodeWorkspaceUnavailable && len(more) == 0 {
				cand.Cleanup()
				return fail(sqlite.RunError, fault.CodeWorkspaceUnavailable, err.Error(), evidence)
			}
			if err != nil {
				cand.Cleanup()
				_ = e.store.Write(ctx, func(tx *sqlite.Tx) error { return tx.FinishRun(fr.runID, sqlite.RunError, err.Error(), e.now()) })
				return res, err
			}
			evidence = append(evidence, more...)
			if v := verification.Judge(fr.policy, more); !v.Passed {
				cand.Cleanup()
				code := fault.CodeVerificationFailed
				if len(v.TimedOut) > 0 {
					code = fault.CodeVerificationTimeout
					res.Message = timeoutMessage(v, fr.policy, taskBudget, regBudget, fr.rec.Task.Size)
				}
				return fail(sqlite.RunFailed, code, "integrated candidate "+short(cand.Revision)+": "+v.Summary, evidence)
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
		// The task is done: its quarantined directories are history.
		_, _ = mgr.Prune(ctx, string(fr.rec.Task.ID), true)
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
		if a.EndedAt == nil {
			return nil
		}
		r, err := tx.GetRecord(a.TaskID)
		if err != nil {
			return err
		}
		if a.EndReason == execution.EndSubmitted {
			// A cohort submission is acknowledged idempotently: the stored
			// submission plus the member's current status, no new authority.
			subs, err := tx.Submissions(a.TaskID)
			if err != nil {
				return err
			}
			proj, err := tx.GetProject(r.Task.ProjectID)
			if err != nil {
				return err
			}
			for _, s := range subs {
				if s.AttemptID != a.ID {
					continue
				}
				st := r.Status(e.now(), proj.MaxAttempts)
				res = VerifyResult{TaskID: a.TaskID, Mode: verification.ModeComplete, Revision: s.Revision, SubmissionID: s.ID, Submitted: true, Replayed: true, Status: st, Completed: st == task.StatusComplete, Passed: st == task.StatusComplete}
				if s.Run != nil {
					res.RunID, res.Summary = s.Run.ID, s.Run.Summary
				}
				switch {
				case r.CompletedAt != nil && r.CompletedSubmissionID == s.ID:
					res.Message = "already submitted and complete; stored result returned"
				case r.Submission != nil && r.Submission.ID != s.ID:
					res.Message = fmt.Sprintf("already submitted (submission %d); a later attempt superseded it, current status %s", s.ID, st)
					res.Completed, res.Passed = false, false
				default:
					res.Message = fmt.Sprintf("already submitted (submission %d); current status %s", s.ID, st)
				}
				ok = true
			}
			return nil
		}
		if a.EndReason != execution.EndFinished {
			return nil
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

// integrateStep names the command that brings the target back into the
// task branch: Worktrunk's rebase step when `wt` is installed, else git.
func integrateStep(target string) string {
	if _, err := exec.LookPath("wt"); err == nil {
		return "`wt step rebase " + target + "`"
	}
	return "`git merge " + target + "`"
}

// pinnedChanges returns the changed paths that fall under a pin (exact
// path, or inside a pinned directory).
func pinnedChanges(pins, changed []string) []string {
	var hit []string
	for _, f := range changed {
		for _, p := range pins {
			if f == p || strings.HasPrefix(f, p+"/") {
				hit = append(hit, f)
				break
			}
		}
	}
	return hit
}

// timeoutMessage explains a timed-out verdict: a task-check timeout is a
// verification-construction problem (or a hang) the agent can fix by
// making the check faster or asking for a larger size; a regression
// timeout is the project's gate being too slow, a planner's problem.
// Neither counts against the attempt.
func timeoutMessage(v verification.Verdict, policy verification.Policy, taskBudget, regBudget time.Duration, size string) string {
	regression := map[string]bool{}
	for _, c := range policy.Regression {
		regression[c.ID] = true
	}
	var taskIDs, regIDs []string
	for _, id := range v.TimedOut {
		if regression[id] {
			regIDs = append(regIDs, id)
		} else {
			taskIDs = append(taskIDs, id)
		}
	}
	if size == "" {
		size = task.SizeSmall
	}
	var parts []string
	if len(taskIDs) > 0 {
		parts = append(parts, fmt.Sprintf("task check(s) %s exceeded the %s budget (%s): the verification is built too slow for this task, or the code it runs hangs; make the check prove only this outcome, split the task, or have the planner set a larger --size", strings.Join(taskIDs, ", "), size, taskBudget))
	}
	if len(regIDs) > 0 {
		parts = append(parts, fmt.Sprintf("regression check(s) %s exceeded the regression budget (%s): the project's gate is too slow; the planner must narrow it (AT_CHANGED_FILES lists what this change touched) or raise --regression-budget", strings.Join(regIDs, ", "), regBudget))
	}
	return "task NOT complete: " + strings.Join(parts, "; ") + " (a timeout is never counted against the attempt)"
}
