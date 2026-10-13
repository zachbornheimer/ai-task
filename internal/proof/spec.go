// Package proof owns the proof contract: the "PRESET: SUBJECT key=value"
// grammar, the sizes that bound a run, and the RunResult every run records.
package proof

import "time"

// ProofBudget is the fixed budget of a --proof run, whatever its size.
const ProofBudget = 30 * time.Second

// Size is the time class of a regression or on_complete run.
type Size string

const (
	SizeSmall  Size = "small"
	SizeMedium Size = "medium"
	SizeLarge  Size = "large"
)

// Budget returns the wall-clock allowance for the size; unknown sizes get
// the small allowance.
func (s Size) Budget() time.Duration {
	switch s {
	case SizeLarge:
		return 900 * time.Second
	case SizeMedium:
		return 300 * time.Second
	default:
		return 60 * time.Second
	}
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
	BudgetMS   int64             `json:"budget_ms"`
	TimedOut   bool              `json:"timed_out"`
	ReturnCode *int              `json:"return_code"`
	Passed     bool              `json:"passed"`
	Output     string            `json:"output"`
}

// OnCompleteBudget is the budget of a waited on-complete action.
const OnCompleteBudget = 900 * time.Second

// ActionSpec is an --on-complete (waited) or --on-complete-detach (spawned,
// not waited on) command that runs after a task completes. It parses with
// the proof grammar.
type ActionSpec struct {
	Preset  string            `json:"preset"`
	Subject string            `json:"subject"`
	Options map[string]string `json:"options"`
	Detach  bool              `json:"detach"`
}

// Proof is the ProofSpec the action runs as.
func (a ActionSpec) Proof() ProofSpec {
	return ProofSpec{Preset: a.Preset, Subject: a.Subject, Options: a.Options}
}

// ParseAction reads "PRESET: SUBJECT [key=value ...]" as an action.
// Unlike a proof, an action is not judged for vacuity: it does things.
func ParseAction(s string, detach bool) (ActionSpec, error) {
	p, err := Parse(s)
	if err != nil {
		return ActionSpec{}, err
	}
	return ActionSpec{Preset: p.Preset, Subject: p.Subject, Options: p.Options, Detach: detach}, nil
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
