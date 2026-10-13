package proof

import (
	"bytes"
	"context"
	"regexp"
	"sync"
	"time"

	"github.com/zachbornheimer/ai-task/internal/checkexec"
)

// OutputTail is how much combined output a RunResult keeps (its last bytes).
const OutputTail = 64 * 1024

// Runner executes specs inside their budget.
type Runner struct {
	registry *Registry
	budget   time.Duration // when > 0, overrides every budget (tests)
}

// RunnerOption configures a Runner.
type RunnerOption func(*Runner)

// WithBudget fixes the budget for every run, replacing the per-type budgets.
func WithBudget(d time.Duration) RunnerOption { return func(r *Runner) { r.budget = d } }

// WithRegistry swaps the preset registry.
func WithRegistry(reg *Registry) RunnerOption { return func(r *Runner) { r.registry = reg } }

func NewRunner(opts ...RunnerOption) *Runner {
	r := &Runner{registry: DefaultRegistry()}
	for _, o := range opts {
		o(r)
	}
	return r
}

func (r *Runner) budgetFor(ct CheckType, spec ProofSpec) time.Duration {
	switch {
	case r.budget > 0:
		return r.budget
	case ct == CheckProof:
		return ProofBudget
	default:
		return spec.Size.Budget()
	}
}

// Run executes spec in workdir. It never returns an error: every failure,
// including an unknown preset or rejected input, is a RunResult with
// Passed=false and the reason in Output. A run past its budget is killed
// (whole process group) and fails with TimedOut set.
func (r *Runner) Run(ctx context.Context, ct CheckType, spec ProofSpec, workdir string) RunResult {
	budget := r.budgetFor(ct, spec)
	res := RunResult{
		CheckType: ct, Preset: spec.Preset, Subject: spec.Subject, Options: spec.Options,
		StartedAt: time.Now().UTC(), BudgetMS: budget.Milliseconds(),
	}
	if res.Options == nil {
		res.Options = map[string]string{}
	}
	argv, err := r.argv(spec, workdir)
	if err != nil {
		res.Output = err.Error()
		return res
	}
	tail := &tailBuffer{limit: OutputTail}
	out := checkexec.Run(ctx, checkexec.Spec{Argv: argv, Dir: workdir, Timeout: budget, Log: tail})
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
	return res
}

func (r *Runner) argv(spec ProofSpec, workdir string) ([]string, error) {
	p, ok := r.registry.Lookup(spec.Preset)
	if !ok {
		return nil, errUnknownPreset(spec.Preset)
	}
	return p.Argv(spec, Env{Workdir: workdir})
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
