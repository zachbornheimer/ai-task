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
