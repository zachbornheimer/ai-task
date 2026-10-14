// Package proof owns the proof contract: the "PRESET: SUBJECT key=value"
// grammar, the sizes that bound a run, and the RunResult every run records.
package proof

import (
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// ProofBudget is the budget of a small check, the default of a --proof.
const ProofBudget = 30 * time.Second

// Size is the time class of any check: its size= option.
type Size string

const (
	SizeSmall  Size = "small"
	SizeMedium Size = "medium"
	SizeLarge  Size = "large"
	// SizeEternal has no budget; the run is still heartbeated and reaped
	// when its owner dies.
	SizeEternal Size = "eternal"
)

// SizeOption is the reserved option every check spec accepts.
const SizeOption = "size"

// Budget returns the wall-clock allowance for the size, zero for eternal;
// unknown sizes get the small allowance.
func (s Size) Budget() time.Duration {
	switch s {
	case SizeEternal:
		return 0
	case SizeLarge:
		return 900 * time.Second
	case SizeMedium:
		return 300 * time.Second
	default:
		return ProofBudget
	}
}

// ParseSize reads a size= value; anything but the four sizes is INVALID_INPUT.
func ParseSize(v string) (Size, error) {
	switch s := Size(v); s {
	case SizeSmall, SizeMedium, SizeLarge, SizeEternal:
		return s, nil
	}
	return "", fault.New(fault.CodeInvalidInput, "size=%q must be small, medium, large or eternal", v)
}

// CheckType says why a run happens, and so which budget governs it.
type CheckType string

const (
	CheckProof      CheckType = "proof"
	CheckRegression CheckType = "regression"
	CheckOnComplete CheckType = "on_complete"
)

// ProofSpec is a parsed "PRESET: SUBJECT [key=value ...]" command.
type ProofSpec struct {
	Preset  string            `json:"preset"`
	Subject string            `json:"subject"`
	Options map[string]string `json:"options"`
	Size    Size              `json:"size"`
}

// RunResult is the one record every run produces. ReturnCode is nil when no
// exit status exists (timed out, killed, never started).
type RunResult struct {
	CheckType  CheckType         `json:"check_type"`
	Preset     string            `json:"preset"`
	Subject    string            `json:"subject"`
	Options    map[string]string `json:"options"`
	StartedAt  time.Time         `json:"started_at"`
	ElapsedMS  int64             `json:"elapsed_ms"`
	BudgetMS   *int64            `json:"budget_ms"` // nil for an eternal check
	TimedOut   bool              `json:"timed_out"`
	ReturnCode *int              `json:"return_code"`
	Passed     bool              `json:"passed"`
	Output     string            `json:"output"`
}

// Budget is the allowance the run had; zero when it had none.
func (r RunResult) Budget() time.Duration {
	if r.BudgetMS == nil {
		return 0
	}
	return time.Duration(*r.BudgetMS) * time.Millisecond
}

// ActionSpec is an --on-complete (waited) or --on-complete-detach (spawned,
// not waited on) command that runs after a task completes. It parses with
// the proof grammar.
type ActionSpec struct {
	Preset  string            `json:"preset"`
	Subject string            `json:"subject"`
	Options map[string]string `json:"options"`
	Detach  bool              `json:"detach"`
	// Size is the action's time class; empty (stored before size=) is large.
	Size Size `json:"size,omitempty"`
}

// Proof is the ProofSpec the action runs as.
func (a ActionSpec) Proof() ProofSpec {
	size := a.Size
	if size == "" {
		size = SizeLarge
	}
	return ProofSpec{Preset: a.Preset, Subject: a.Subject, Options: a.Options, Size: size}
}

// ParseAction reads "PRESET: SUBJECT [key=value ...]" as an action.
// Unlike a proof, an action is not judged for vacuity: it does things.
func ParseAction(s string, detach bool) (ActionSpec, error) {
	p, err := ParseDefaulting(s, SizeLarge)
	if err != nil {
		return ActionSpec{}, err
	}
	return ActionSpec{Preset: p.Preset, Subject: p.Subject, Options: p.Options, Detach: detach, Size: p.Size}, nil
}

// ActionStatus is where an action stands.
type ActionStatus string

const (
	ActionPending   ActionStatus = "pending"
	ActionRunning   ActionStatus = "running"
	ActionDetached  ActionStatus = "detached"
	ActionSucceeded ActionStatus = "succeeded"
	ActionFailed    ActionStatus = "failed"
)

// Retryable reports whether `verify complete` runs the action again: it
// never ran, failed, or was interrupted mid-run.
func (s ActionStatus) Retryable() bool {
	return s == ActionPending || s == ActionFailed || s == ActionRunning
}

// ActionResult is an action and its latest run. Run is nil until it ran.
type ActionResult struct {
	Spec     ActionSpec   `json:"spec"`
	Status   ActionStatus `json:"status"`
	Attempts int          `json:"attempts"`
	Run      *RunResult   `json:"run"`
}
