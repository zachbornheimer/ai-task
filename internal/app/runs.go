package app

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/proof"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// runner executes checks under the contract budgets (proof 30 s, regression
// by size); a configured ProofBudget replaces them all.
func (e *Engine) runner() *proof.Runner {
	if e.proofBudget > 0 {
		return proof.NewRunner(proof.WithBudget(e.proofBudget))
	}
	return proof.NewRunner()
}

// checkType is the kind of run a planned step is.
func checkType(step verification.Step) proof.CheckType {
	if step.Regression {
		return proof.CheckRegression
	}
	return proof.CheckProof
}

// stepSpec is the ProofSpec a stored command check stands for: the raw
// command through the "run" preset.
func stepSpec(c verification.CheckSpec, size proof.Size) proof.ProofSpec {
	return proof.ProofSpec{Preset: "run", Subject: shellJoin(c.Command), Options: map[string]string{}, Size: size}
}

// runResultsOf lists the RunResult of each evidence row that has one.
func runResultsOf(evidence []verification.Evidence) []proof.RunResult {
	out := []proof.RunResult{}
	for i := range evidence {
		ev := &evidence[i]
		if ev.Run != nil {
			out = append(out, *ev.Run)
		}
	}
	return out
}

// runChecks executes a policy's checks in dir in planner order, committing
// each evidence row (and its RunResult) as it lands. fence is evaluated in
// each evidence transaction; a failure (lost authority, superseded run)
// stops the run. Each check gets its contract budget; the suite shares one
// wall-clock allowance on top: a check that would start after it is spent
// is recorded as timed out without running.
func (e *Engine) runChecks(ctx context.Context, dir string, policy verification.Policy, runID int64, revision string, fence func(*sqlite.Tx) error, opts runOpts) ([]verification.Evidence, error) {
	var out []verification.Evidence
	requiredTaskFailed := false
	var deadline time.Time
	if opts.budget > 0 {
		deadline = time.Now().Add(opts.budget)
	}
	extra := changedFilesEnv(dir, opts)
	runner := e.runner()
	for _, step := range verification.Plan(policy) {
		if err := ctx.Err(); err != nil {
			return out, fault.WrapInternal(err, "verification interrupted")
		}
		var suiteLeft time.Duration
		if !deadline.IsZero() {
			suiteLeft = time.Until(deadline)
		}
		run := stepRun{runner: runner, size: opts.size, suiteLeft: suiteLeft, suiteSet: !deadline.IsZero(), env: extra, logDir: opts.logDir}
		run.suiteLimited = run.suiteSet && suiteLeft < shortest(runner.Budget(stepSpec(step.Check, opts.size)), step.Check.Timeout)
		ev := e.execStep(ctx, dir, step, runID, revision, policy.Digest(), requiredTaskFailed, run)
		// A check recorded without running keeps its own message (execStep
		// says why); these describe a check that started.
		if ev.Run != nil && ev.Outcome == verification.OutcomeTimeout {
			ev.Message = timeoutReason(ev.Run, run, opts)
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

// stepRun is how one step runs: who runs it, its size, how much of the
// suite allowance is left (suiteSet false = unbounded), and its surroundings.
type stepRun struct {
	runner    *proof.Runner
	size      proof.Size
	suiteLeft time.Duration
	suiteSet  bool
	// suiteLimited: the suite's remaining allowance, not the check's own
	// budget, is what bounds this run.
	suiteLimited bool
	env          []string
	logDir       string
}

func timeoutReason(r *proof.RunResult, run stepRun, opts runOpts) string {
	if run.suiteLimited {
		return fmt.Sprintf("exceeded %s (%s for the whole suite); the check is too slow for the task's size, or the code it runs hangs", opts.budgetName, opts.budget)
	}
	return fmt.Sprintf("exceeded its %s budget (%s); the check is too slow for the contract, or the code it runs hangs", r.CheckType, r.Budget())
}

func (e *Engine) execStep(ctx context.Context, dir string, step verification.Step, runID int64, revision, policyDigest string, skipRegression bool, run stepRun) verification.Evidence {
	c := step.Check
	now := e.now()
	ev := verification.Evidence{RunID: runID, CheckID: c.ID, CheckVersion: c.Version, CheckDigest: c.Digest(), Required: c.Required, Revision: revision, PolicyDigest: policyDigest, StartedAt: now, FinishedAt: now, ExitCode: -1}
	if step.Regression && skipRegression {
		ev.Outcome = verification.OutcomeSkipped
		ev.Message = "skipped: a required task check failed"
		return ev
	}
	if run.suiteSet && run.suiteLeft <= 0 {
		ev.Outcome = verification.OutcomeTimeout
		ev.Message = "not run: the suite's budget was spent by earlier checks"
		return ev
	}
	if c.Eval != nil {
		return e.execEvaluator(ctx, dir, step, ev, run)
	}
	spec := stepSpec(c, run.size)
	cmd := proof.Command{Argv: c.Command, Dir: filepath.Join(dir, c.Dir), Env: append(checkEnv(dir), run.env...), Cap: shortest(c.Timeout, run.suiteLeft)}
	logFile, logPath := openCheckLog(run.logDir, c.ID)
	if logFile != nil {
		defer func() { _ = logFile.Close() }() // advisory log: the check result is authoritative
		where := c.Dir
		if where == "" {
			where = "."
		}
		discard.Writef(logFile, "$ %s\n# check %s in %s at %s\n\n", shellJoin(c.Command), c.ID, where, revision)
		cmd.Log, ev.Log = logFile, logPath
	}
	rr, res := run.runner.RunCommand(ctx, checkType(step), spec, cmd)
	ev.Run = &rr
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
	if logFile != nil {
		discard.Writef(logFile, "\n# %s: exit=%d after %s", ev.Outcome, ev.ExitCode, ev.FinishedAt.Sub(ev.StartedAt).Round(time.Millisecond))
		if ev.Message != "" {
			discard.Writef(logFile, " (%s)", ev.Message)
		}
		discard.Writeln(logFile)
	}
	return ev
}

// shortest is the smaller positive duration; zero when neither is positive.
func shortest(a, b time.Duration) time.Duration {
	switch {
	case a <= 0:
		return max(b, 0)
	case b <= 0:
		return a
	default:
		return min(a, b)
	}
}

// execEvaluator runs an in-process proof (grep, file_exists, ...) through the
// Runner's Evaluator path and records its RunResult: return_code 0 when it
// held, 1 when it did not, and the reason as the output.
func (e *Engine) execEvaluator(ctx context.Context, dir string, step verification.Step, ev verification.Evidence, run stepRun) verification.Evidence {
	spec := *step.Check.Eval
	if spec.Size == "" {
		spec.Size = run.size
	}
	rr := run.runner.Run(ctx, checkType(step), spec, dir)
	code := 1
	if rr.Passed {
		code = 0
	}
	rr.ReturnCode = &code
	ev.Run = &rr
	ev.StartedAt = rr.StartedAt
	ev.FinishedAt = rr.StartedAt.Add(time.Duration(rr.ElapsedMS) * time.Millisecond)
	ev.ExitCode, ev.Stdout = code, rr.Output
	switch {
	case rr.TimedOut:
		ev.Outcome, ev.Message = verification.OutcomeTimeout, rr.Output
	case rr.Passed:
		ev.Outcome = verification.OutcomePassed
	default:
		ev.Outcome, ev.Message = verification.OutcomeFailed, rr.Output
	}
	return ev
}
