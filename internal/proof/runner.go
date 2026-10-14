package proof

import (
	"bytes"
	"context"
	"io"
	"regexp"
	"sync"
	"time"

	"github.com/zachbornheimer/ai-task/internal/process"
)

// OutputTail is how much combined output a RunResult keeps (its last bytes).
const OutputTail = 64 * 1024

// Runner executes specs inside their budget.
type Runner struct {
	registry *Registry
	target   string        // target branch handed to presets
	budget   time.Duration // when > 0, overrides every budget (tests)
	hasMise  func(workdir string) bool
	env      []string // when non-nil, the whole environment of every process
}

// RunnerOption configures a Runner.
type RunnerOption func(*Runner)

// WithBudget fixes the budget for every run, replacing the per-type budgets.
func WithBudget(d time.Duration) RunnerOption { return func(r *Runner) { r.budget = d } }

// WithRegistry swaps the preset registry.
func WithRegistry(reg *Registry) RunnerOption { return func(r *Runner) { r.registry = reg } }

// WithEnv fixes the complete environment every process of the runner gets.
func WithEnv(env []string) RunnerOption { return func(r *Runner) { r.env = env } }

// WithTargetBranch tells presets which branch the project targets.
func WithTargetBranch(b string) RunnerOption { return func(r *Runner) { r.target = b } }

func NewRunner(opts ...RunnerOption) *Runner {
	r := &Runner{registry: DefaultRegistry(), hasMise: HasMiseConfig}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Command is a stored check that already is an argv: it runs as given, in
// Dir with Env, teeing its output to Log, under the budget of its CheckType
// (Cap, when positive, can only shorten it).
type Command struct {
	Argv []string
	Dir  string
	Env  []string
	Log  io.Writer
	Cap  time.Duration
}

// Budget returns the allowance a run of spec gets: the spec's own size,
// zero when it has none (eternal).
func (r *Runner) Budget(spec ProofSpec) time.Duration {
	if r.budget > 0 {
		return r.budget
	}
	return spec.Size.Budget()
}

// Run executes spec in workdir. It never returns an error: every failure,
// including an unknown preset or rejected input, is a RunResult with
// Passed=false and the reason in Output. A run past its budget is killed
// (whole process group) and fails with TimedOut set.
func (r *Runner) Run(ctx context.Context, ct CheckType, spec ProofSpec, workdir string) RunResult {
	if p, ok := r.registry.Lookup(spec.Preset); ok {
		if ev, isEval := p.(Evaluator); isEval {
			res := newRunResult(ct, spec, r.Budget(spec))
			return r.evaluate(ev, spec, workdir, res)
		}
	}
	argv, err := r.argv(spec, workdir)
	if err != nil {
		return r.rejected(ct, spec, err)
	}
	res, _ := r.RunCommand(ctx, ct, spec, Command{Argv: argv, Dir: workdir, Env: r.env})
	return res
}

func (r *Runner) rejected(ct CheckType, spec ProofSpec, err error) RunResult {
	res := newRunResult(ct, spec, r.Budget(spec))
	res.Output = err.Error()
	return res
}

func newRunResult(ct CheckType, spec ProofSpec, budget time.Duration) RunResult {
	budgetMS := budget.Milliseconds()
	res := RunResult{
		CheckType: ct, Preset: spec.Preset, Subject: spec.Subject, Options: spec.Options,
		StartedAt: time.Now().UTC(), BudgetMS: &budgetMS,
	}
	if budget <= 0 {
		res.BudgetMS = nil
	}
	if res.Options == nil {
		res.Options = map[string]string{}
	}
	return res
}

// RunCommand executes cmd as the proof of spec, with the same budget,
// kill, and recording rules as Run. The raw process result comes back too,
// for callers that keep stdout and stderr apart.
func (r *Runner) RunCommand(ctx context.Context, ct CheckType, spec ProofSpec, cmd Command) (RunResult, process.Result) {
	budget := r.Budget(spec)
	if r.budget <= 0 && cmd.Cap > 0 && (budget <= 0 || cmd.Cap < budget) {
		budget = cmd.Cap
	}
	res := newRunResult(ct, spec, budget)
	tail := &tailBuffer{limit: OutputTail}
	var log io.Writer = tail
	if cmd.Log != nil {
		log = io.MultiWriter(tail, cmd.Log)
	}
	argv := cmd.Argv
	if r.hasMise(cmd.Dir) {
		argv = wrapMise(argv)
	}
	out := process.Run(ctx, process.Spec{Argv: argv, Dir: cmd.Dir, Env: cmd.Env, Timeout: processTimeout(budget), Log: log})
	return r.finishRun(res, spec, cmd.Dir, out, tail), out
}

func (r *Runner) finishRun(res RunResult, spec ProofSpec, workdir string, out process.Result, tail *tailBuffer) RunResult {
	res.StartedAt = out.StartedAt
	res.ElapsedMS = out.FinishedAt.Sub(out.StartedAt).Milliseconds()
	res.TimedOut = out.TimedOut
	res.Output = tail.String()
	if !out.Started {
		res.Output = out.Err.Error()
		return res
	}
	if out.ExitCode >= 0 && !out.TimedOut {
		code := out.ExitCode
		res.ReturnCode = &code
	}
	res.Passed = res.ReturnCode != nil && *res.ReturnCode == 0 && matchesExpect(spec, out.Stdout)
	if p, ok := r.registry.Lookup(spec.Preset); ok && res.ReturnCode != nil {
		if b, ok := p.(EnvBinder); ok {
			p = b.ForEnv(Env{Workdir: workdir, TargetBranch: r.target})
		}
		if j, ok := p.(Judger); ok {
			passed, reason := j.Judge(spec, []byte(res.Output), *res.ReturnCode)
			res.Passed = res.Passed && passed
			if reason != "" {
				res.Output += "\n" + reason
			}
		}
	}
	return res
}

func (r *Runner) argv(spec ProofSpec, workdir string) ([]string, error) {
	p, ok := r.registry.Lookup(spec.Preset)
	if !ok {
		return nil, errUnknownPreset(spec.Preset)
	}
	return p.Argv(spec, Env{Workdir: workdir, TargetBranch: r.target})
}

// matchesExpect applies the generic expect=REGEX option to stdout.
func matchesExpect(spec ProofSpec, stdout string) bool {
	expr, ok := spec.Options["expect"]
	if !ok {
		return true
	}
	re, err := regexp.Compile(expr)
	return err == nil && re.MatchString(stdout)
}

// tailBuffer keeps the last limit bytes written.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.limit; over > 0 {
		t.buf = bytes.Clone(t.buf[over:])
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// evaluate runs an in-process Evaluator, failing the run when it outlasts the budget.
func (r *Runner) evaluate(ev Evaluator, spec ProofSpec, workdir string, res RunResult) RunResult {
	type verdict struct {
		passed bool
		reason string
		err    error
	}
	done := make(chan verdict, 1)
	go func() {
		passed, reason, err := ev.Evaluate(spec, workdir)
		done <- verdict{passed, reason, err}
	}()
	var expired <-chan time.Time
	if budget := res.Budget(); budget > 0 {
		timer := time.NewTimer(budget)
		defer timer.Stop()
		expired = timer.C
	}
	select {
	case v := <-done:
		res.Passed, res.Output = v.passed, v.reason
		if v.err != nil {
			res.Passed, res.Output = false, v.err.Error()
		}
	case <-expired:
		res.TimedOut, res.Output = true, "evaluation exceeded the budget"
	}
	res.ElapsedMS = time.Since(res.StartedAt).Milliseconds()
	return res
}

// processTimeout is the process deadline for a budget; no budget means none.
func processTimeout(budget time.Duration) time.Duration {
	if budget <= 0 {
		return process.NoTimeout
	}
	return budget
}
