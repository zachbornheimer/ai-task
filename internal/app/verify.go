package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/proof"
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
//   - ModeProof / ModeRegression run that category fresh in the attempt's
//     workspace (editable, so results are diagnostic) and never complete,
//     release, or integrate.
//   - ModeComplete records an immutable submission (a clean commit in Git
//     projects), runs BOTH categories fresh on a detached snapshot of that
//     revision, integrates when the project requires it, and then, in one
//     short transaction that re-checks authority, prerequisites, contract
//     and policy, records completion and ends the claim.
//
// Every invocation executes checks; stored evidence is never reused as
// proof. A failing result is returned as CHECK_FAILED with the
// result in Details; the claim stays live so the agent can repair.
func (e *Engine) Verify(ctx context.Context, token execution.Token, mode verification.Mode) (VerifyResult, error) {
	if _, err := verification.ParseMode(string(mode)); err != nil {
		return VerifyResult{}, err
	}
	if err := e.reconcile(ctx); err != nil {
		return VerifyResult{}, err
	}
	var res VerifyResult
	var err error
	if mode == verification.ModeComplete {
		res, err = e.verifyComplete(ctx, token)
		if err == nil && res.Completed {
			err = e.attachActions(ctx, token, &res)
		}
		if err == nil {
			err = e.attachCheckChanges(ctx, token, &res)
		}
	} else {
		res, err = e.verifyDiagnostic(ctx, token, mode)
	}
	writeRunResult(res, err)
	return res, err
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
		return p, fault.New(fault.CodeNotReady, "attempt has no worktree; claim the task again")
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
		return VerifyResult{}, fault.New(fault.CodeInvalidInput, "%s: %v; `verify complete` will fail closed", p.rec.Task.ID, fault.MessageOf(err))
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
		runID, err = tx.InsertRun(sqlite.NewRun{TaskID: p.rec.Task.ID, AttemptID: p.attempt.ID, Mode: mode, Status: sqlite.RunRunning, Revision: revision, Policy: policy, Environment: environmentFingerprint(), HeartbeatEvery: e.renewalInterval(p.attempt.Lease)}, now)
		return err
	})
	if err != nil {
		return VerifyResult{}, err
	}
	opCtx, stop := e.heartbeatWithPulse(ctx, token, p.attempt.Lease, runID)
	fence := func(tx *sqlite.Tx) error { _, err := e.authorize(tx, token, e.now()); return err }
	changed, _ := workspace.ChangedFiles(ctx, p.dir, "refs/heads/"+p.proj.TargetBranch)
	if st, err := workspace.Inspect(ctx, p.dir); err == nil {
		for _, line := range st.Dirty {
			if len(line) > 3 {
				changed = append(changed, line[3:])
			}
		}
	}
	taskOpts, regOpts := e.suiteOpts(p.rec.Task, p.proj, changed)
	logDir := e.runLogDir(p.proj.ID, runID)
	taskOpts.logDir, regOpts.logDir = logDir, logDir
	opts := taskOpts
	if mode == verification.ModeRegression {
		opts = regOpts
	}
	evidence, err := e.runChecks(opCtx, p.dir, policy, runID, revision, fence, opts)
	if terr := stop(); terr != nil && err != nil {
		err = fault.Wrap(terr, fault.CodeOf(terr), "verification aborted: claim authority was lost while checks ran")
	}
	res := VerifyResult{TaskID: p.rec.Task.ID, Mode: mode, RunID: runID, Revision: revision, Evidence: evidence, LogDir: logDir}
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
		res.Message = timeoutMessage(verdict, policy, regOpts.budget)
		return res, &fault.Error{Code: fault.CodeCheckFailed, Message: fmt.Sprintf("%s checks timed out for %s: %s", mode, p.rec.Task.ID, summary), Details: res}
	}
	if !verdict.Passed {
		return res, &fault.Error{Code: fault.CodeCheckFailed, Message: fmt.Sprintf("%s checks failed for %s: %s", mode, p.rec.Task.ID, summary), Details: res}
	}
	return res, nil
}

// runSuites runs a policy's two categories on separate, disposable
// snapshots of one revision: task checks first, then (unless a required
// task check failed, in which case the regression rows are recorded as
// skipped) the regression checks on a fresh snapshot. Nothing a task check
// leaves behind can be the reason a regression check passes.
func (e *Engine) runSuites(ctx context.Context, mgr workspace.Manager, revision string, policy verification.Policy, runID int64, fence func(*sqlite.Tx) error, t task.Task, proj project.Project, workDir string) ([]verification.Evidence, error) {
	var out []verification.Evidence
	inPlace := proj.Rootless()
	var changed []string
	if !inPlace {
		changed, _ = workspace.ChangedBetween(ctx, mgr.Repo, "refs/heads/"+proj.TargetBranch, revision)
	}
	taskOpts, regOpts := e.suiteOpts(t, proj, changed)
	logDir := e.runLogDir(proj.ID, runID)
	taskOpts.logDir, regOpts.logDir = logDir, logDir
	taskPolicy := verification.Policy{TaskChecks: policy.TaskChecks}
	if len(taskPolicy.TaskChecks) > 0 {
		dir, cleanup := workDir, func() {}
		if !inPlace {
			var err error
			dir, cleanup, err = mgr.Snapshot(ctx, revision)
			if err != nil {
				return nil, err
			}
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
	for i := range out {
		ev := &out[i]
		if ev.Required && ev.Outcome != verification.OutcomePassed {
			requiredTaskFailed = true
		}
	}
	regPolicy := verification.Policy{Regression: policy.Regression}
	if requiredTaskFailed {
		ev, err := e.runChecksSkipping(ctx, regPolicy, runID, revision, policy.Digest(), fence)
		return append(out, ev...), err
	}
	if inPlace {
		ev, err := e.runChecks(ctx, workDir, regPolicy, runID, revision, fence, regOpts)
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
		ev := e.execStep(ctx, "", step, runID, revision, digest, true, stepRun{})
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
// ATASK_CHANGED_FILES so a regression check can target affected tests.
type runOpts struct {
	budget     time.Duration
	budgetName string
	changed    []string
	target     string
	// logDir receives each check's full output; empty writes none.
	logDir string
	// size picks the regression budget (small, medium, large).
	size proof.Size
	// unrestricted runs checks without confinement. A rootless project has
	// no repository, so its claim workspace is just the directory the task
	// was claimed from: confining checks to it guards nothing, and their
	// proofs routinely reach other checkouts and config paths.
	unrestricted bool
}

// suiteOpts derives the task-check and regression allowances for a task
// from its size and its regression proof's size.
func (e *Engine) suiteOpts(t task.Task, proj project.Project, changed []string) (taskOpts, regOpts runOpts) {
	size, regSize := checkSizes(t)
	unrestricted := proj.Rootless()
	taskOpts = runOpts{budget: e.sizeBudget(size), budgetName: fmt.Sprintf("the %s task-check budget", size), changed: changed, target: proj.TargetBranch, size: size, unrestricted: unrestricted}
	regOpts = runOpts{budget: e.sizeBudget(regSize), budgetName: "the regression budget", changed: changed, target: proj.TargetBranch, size: regSize, unrestricted: unrestricted}
	return taskOpts, regOpts
}

// checkEnv is the environment checks and actions run with: the allowlisted
// part of the parent environment (see CheckEnvironment), GOFLAGS carrying
// -count=1 so Go's own test cache cannot turn a fresh run into a replay,
// and a TMPDIR private to the snapshot so temporary files of one suite
// never reach another.
func checkEnv(dir string) []string {
	extra, _ := proof.LoadExtraEnv(proof.ConfigPath()) // a bad config is reported where presets load
	env := NewCheckEnvironment(extra).Filter(os.Environ())
	flags := "-count=1"
	kept := env[:0:0]
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "GOFLAGS="):
			// An at check can run a suite that itself runs at checks: keep
			// the caller's flags and add -count=1 only when it is missing.
			flags = strings.TrimSpace(strings.TrimPrefix(kv, "GOFLAGS="))
			if !hasField(flags, "-count=1") {
				flags = strings.TrimSpace(flags + " -count=1")
			}
		case strings.HasPrefix(kv, "TMPDIR="):
		default:
			kept = append(kept, kv)
		}
	}
	tmp := dir + ".tmp"
	if dir == "" {
		tmp = os.TempDir()
	} else {
		_ = os.MkdirAll(tmp, 0o700)
	}
	return append(kept, "GOFLAGS="+flags, "ATASK_CHECK_DIR="+dir, "TMPDIR="+tmp)
}

// ensureCheckTmp creates the TMPDIR checkEnv gives checks running in dir
// and proves it writable.
func ensureCheckTmp(dir string) error {
	tmp := dir + ".tmp"
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return fault.Wrap(err, fault.CodeInvalidInput, "checks run here get %s as TMPDIR and it cannot be created; claim from a directory whose parent is writable", tmp)
	}
	f, err := os.CreateTemp(tmp, ".atask-probe-")
	if err != nil {
		return fault.Wrap(err, fault.CodeInvalidInput, "checks run here get %s as TMPDIR and it is not writable", tmp)
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// changedFilesEnv writes the candidate's changed paths next to the
// snapshot and names the file and the target branch for the checks.
// Always non-nil so execStep can tell a spent budget from "no budget".
func changedFilesEnv(dir string, opts runOpts) []string {
	env := []string{"ATASK_TARGET_BRANCH=" + opts.target}
	tmp := os.TempDir()
	if dir != "" {
		tmp = dir + ".tmp"
		_ = os.MkdirAll(tmp, 0o700)
	}
	path := filepath.Join(tmp, "at-changed-files")
	if err := os.WriteFile(path, []byte(strings.Join(opts.changed, "\n")+"\n"), 0o600); err == nil {
		env = append(env, "ATASK_CHANGED_FILES="+path)
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
	for i := range evidence {
		ev := &evidence[i]
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
	// checkCtx is canceled by the heartbeat when authority is lost, so
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
		return VerifyResult{}, fault.New(fault.CodeInvalidInput, "%s cannot complete: no %s are defined (completion fails closed)", p.rec.Task.ID, missing)
	}
	if p.rec.UnmetRequires > 0 {
		return VerifyResult{}, fault.New(fault.CodeNotReady, "%s cannot complete: %d prerequisite(s) are not complete", p.rec.Task.ID, p.rec.UnmetRequires)
	}
	revision := "general"
	if !p.proj.Rootless() {
		st, err := workspace.RequireClean(ctx, p.dir)
		if err != nil {
			return VerifyResult{}, err
		}
		revision = st.Revision
	}
	fr := finalRun{prepared: p, token: token, revision: revision, contractRev: p.rec.Task.ContractRev, regressionID: (verification.Policy{Regression: p.policy.Regression}).Digest()}
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
			return fault.New(fault.CodeNotReady, "%s cannot complete: prerequisites changed", r.Task.ID)
		}
		// One final run per attempt at a time: a double-fired `verify
		// complete` must neither race its twin nor count a failure.
		if s := r.Submission; s != nil && s.AttemptID == p.attempt.ID && s.RunStatus() == sqlite.RunRunning {
			return fault.New(fault.CodeConflict, "%s: a `verify complete` run (%d) is already in progress for this attempt; wait for its result", r.Task.ID, s.Run.ID)
		}
		if fr.submission, err = tx.InsertSubmission(r.Task.ID, p.attempt.ID, revision, now); err != nil {
			return err
		}
		fr.runID, err = tx.InsertRun(sqlite.NewRun{TaskID: r.Task.ID, AttemptID: p.attempt.ID, SubmissionID: fr.submission, Mode: verification.ModeComplete, Status: sqlite.RunRunning, Revision: revision, Policy: p.policy, Environment: environmentFingerprint(), HeartbeatEvery: e.renewalInterval(p.attempt.Lease)}, now)
		return err
	})
	if err != nil {
		return VerifyResult{}, err
	}
	e.notify()
	opCtx, stop := e.heartbeatWithPulse(ctx, token, p.attempt.Lease, fr.runID)
	fr.checkCtx = opCtx
	res, err := e.executeFinal(ctx, fr)
	if terr := stop(); terr != nil && err != nil && opCtx.Err() != nil && ctx.Err() == nil {
		err = fault.Wrap(terr, fault.CodeOf(terr), "verification aborted: claim authority was lost while checks ran")
	}
	e.notify()
	return res, err
}

// executeFinal runs both suites on an immutable snapshot, integrates, and
// finalizes. It is used by the single-task path; cohorts have their own.
func (e *Engine) executeFinal(ctx context.Context, fr finalRun) (VerifyResult, error) {
	res := VerifyResult{TaskID: fr.rec.Task.ID, Mode: verification.ModeComplete, RunID: fr.runID, Revision: fr.revision, SubmissionID: fr.submission, LogDir: e.runLogDir(fr.proj.ID, fr.runID)}
	fence := func(tx *sqlite.Tx) error {
		if _, err := e.authorize(tx, fr.token, e.now()); err != nil {
			return err
		}
		latest, err := tx.LatestRunID(fr.submission)
		if err != nil {
			return err
		}
		if latest != fr.runID {
			return fault.New(fault.CodeInvalidSession, "run %d superseded", fr.runID)
		}
		return nil
	}
	// fail records why the run did not complete. Only a genuine failed
	// proof counts against the attempt (once per attempt, fenced to the
	// current generation); environment and authority problems are kept
	// as last_error so the next attempt and humans can see them.
	failAs := func(class failureClass, status sqlite.RunStatus, code fault.Code, summary string, evidence []verification.Evidence) (VerifyResult, error) {
		now := e.now()
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
		case code == fault.CodeCheckFailed && class != classAttempt:
			// a check ran out of budget: message set by the caller
		case class == classAttempt:
			res.Message = "task NOT complete: a required check failed; the claim stays live for repair (counted against the attempt budget once)"
		case class == classEnvironment:
			res.Message = "task NOT complete: the environment stopped the run, not the checks; fix the cause and verify again (not counted as a failure)"
		default:
			res.Message = "task NOT complete: authority or contract changed during verification; verify again under the current contract (not counted as a failure)"
		}
		return res, &fault.Error{Code: code, Message: fmt.Sprintf("%s: %s", fr.rec.Task.ID, summary), Details: res}
	}
	fail := func(status sqlite.RunStatus, code fault.Code, summary string, evidence []verification.Evidence) (VerifyResult, error) {
		return failAs(classify(code), status, code, summary, evidence)
	}
	// failTimeout: a check ran out of its budget. The verification was
	// built too slow for the task (or hangs); never a strike.
	failTimeout := func(summary string, evidence []verification.Evidence) (VerifyResult, error) {
		return failAs(classEnvironment, sqlite.RunFailed, fault.CodeCheckFailed, summary, evidence)
	}
	mgr := e.manager(fr.proj)
	checkCtx := fr.checkCtx
	if checkCtx == nil {
		checkCtx = ctx
	}
	// Immutable inputs: detached snapshots of the submitted revision, one
	// per check category.
	evidence, err := e.runSuites(checkCtx, mgr, fr.revision, fr.policy, fr.runID, fence, fr.rec.Task, fr.proj, fr.attempt.WorkspacePath)
	if err != nil && fault.CodeOf(err) == fault.CodeNotReady && len(evidence) == 0 {
		return fail(sqlite.RunError, fault.CodeNotReady, err.Error(), nil)
	}
	if err != nil {
		res.Evidence = evidence
		_ = e.store.Write(ctx, func(tx *sqlite.Tx) error { return tx.FinishRun(fr.runID, sqlite.RunError, err.Error(), e.now()) })
		return res, err
	}
	verdict := verification.Judge(fr.policy, evidence)
	proofSize, regSize := checkSizes(fr.rec.Task)
	taskBudget, regBudget := e.sizeBudget(proofSize), e.sizeBudget(regSize)
	if len(verdict.TimedOut) > 0 {
		res.Message = timeoutMessage(verdict, fr.policy, regBudget)
		return failTimeout(verdict.Summary+"; "+res.Message, evidence)
	}
	if !verdict.Passed {
		return fail(sqlite.RunFailed, fault.CodeCheckFailed, verdict.Summary, evidence)
	}
	res.Warnings = budgetWarnings(evidence, fr.policy, taskBudget, regBudget)
	spec := completionSpec{proj: fr.proj, taskID: fr.rec.Task.ID, attemptID: fr.attempt.ID, runID: fr.runID, submission: fr.submission, owner: fr.token.Digest(), contractRev: fr.contractRev, regression: fr.regressionID, policy: fr.policy, source: fr.revision}
	done := func() (VerifyResult, error) {
		res.Evidence, res.Passed, res.Completed = evidence, true, true
		res.Status = task.StatusComplete
		res.Summary = verdict.Summary
		res.Message = "task complete; claim ended"
		return res, nil
	}
	// refused maps a finalization refusal (changed contract, lost
	// authority, ...) to the caller's error.
	refused := func(err error) (VerifyResult, error) {
		if fault.IsInternal(err) {
			return res, err
		}
		return fail(sqlite.RunFailed, fault.CodeOf(err), err.Error(), evidence)
	}
	if err := e.fault("final:after-checks"); err != nil {
		return res, err
	}
	// One transaction re-checks everything completion depends on and
	// records it.
	now := e.now()
	err = e.store.Write(ctx, func(tx *sqlite.Tx) error {
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
	// The task is done: its quarantined directories are history.
	_, _ = mgr.Prune(ctx, string(fr.rec.Task.ID), true)
	return done()
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
			res = VerifyResult{TaskID: a.TaskID, Mode: verification.ModeComplete, RunID: s.Run.ID, Revision: s.Revision, SubmissionID: s.ID, Passed: true, Completed: true, Status: task.StatusComplete, Summary: s.Run.Summary, Evidence: ev, Replayed: true, Message: "already complete; stored result returned, no checks re-run"}
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
// timeoutMessage explains a timed-out verdict: a task-check timeout is a
// verification-construction problem (or a hang) the agent can fix by
// making the check faster or asking for a larger size; a regression
// timeout is the project's gate being too slow, a planner's problem.
// Neither counts against the attempt.
func timeoutMessage(v verification.Verdict, policy verification.Policy, regBudget time.Duration) string {
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
	var parts []string
	if len(taskIDs) > 0 {
		parts = append(parts, fmt.Sprintf("task check(s) %s exceeded the proof's size budget: narrow the proof command, set size=medium|large on the proof, or move the long run to --regression", strings.Join(taskIDs, ", ")))
	}
	if len(regIDs) > 0 {
		parts = append(parts, fmt.Sprintf("regression check(s) %s exceeded the regression budget (%s): the regression is too slow for its size; set size=medium|large (300s/900s) on the regression, e.g. --regression 'run: go test ./... size=large', or narrow the command (ATASK_CHANGED_FILES lists what this change touched)", strings.Join(regIDs, ", "), regBudget))
	}
	return "task NOT complete: " + strings.Join(parts, "; ") + " (a timeout is never counted against the attempt)"
}

// hasField reports whether the whitespace-separated list s contains f.
func hasField(s, f string) bool {
	return slices.Contains(strings.Fields(s), f)
}
