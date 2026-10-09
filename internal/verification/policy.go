// Package verification owns the verification contract: which checks must pass
// for a task to count as verified, how that contract is represented durably,
// and how it is versioned.
//
// Milestone 1 stores and versions policies. Executing checks, planning, and
// evidence arrive in Milestone 2 and live in this package too.
package verification

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// MaxCheckTimeout bounds a single check. Checks are external processes; an
// unbounded check would hold a submission in "running" forever.
const MaxCheckTimeout = 24 * time.Hour

// DefaultCheckTimeout applies when a CheckSpec has no timeout.
const DefaultCheckTimeout = 30 * time.Minute

var checkIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// CheckSpec is a durable description of one external check. It is a value
// object: a command line, where to run it, how long it may take, and whether
// a failure blocks completion.
//
// Go function values are deliberately not part of this type: a policy must be
// representable in SQLite and readable by a process that did not create it.
type CheckSpec struct {
	// ID is unique within a policy and stable across versions of the check.
	ID string
	// Version distinguishes revisions of the same check (free-form, e.g. "2").
	// Changing the command without changing Version is tolerated because the
	// policy digest covers the full content; Version is for human bookkeeping
	// and evidence reuse rules.
	Version string
	// Command is an argv array. It is never passed to a shell.
	Command []string
	// Dir is the working directory relative to the workspace root ("" = root).
	Dir string
	// Timeout bounds the check; zero means DefaultCheckTimeout.
	Timeout time.Duration
	// Required checks must pass for the task to be verified. Optional checks
	// are recorded as evidence but never compensate for a failed required one.
	Required bool
}

// checkJSON is the durable wire form. Timeout is an integer millisecond count
// so that the representation is stable across languages and never depends on
// Go's Duration formatting.
type checkJSON struct {
	ID        string   `json:"id"`
	Version   string   `json:"version,omitempty"`
	Command   []string `json:"command"`
	Dir       string   `json:"dir,omitempty"`
	TimeoutMS int64    `json:"timeout_ms,omitempty"`
	Required  bool     `json:"required"`
}

// MarshalJSON writes the durable form.
func (c CheckSpec) MarshalJSON() ([]byte, error) {
	return json.Marshal(checkJSON{
		ID: c.ID, Version: c.Version, Command: c.Command, Dir: c.Dir,
		TimeoutMS: c.Timeout.Milliseconds(), Required: c.Required,
	})
}

// UnmarshalJSON reads the durable form.
func (c *CheckSpec) UnmarshalJSON(b []byte) error {
	var w checkJSON
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*c = CheckSpec{
		ID: w.ID, Version: w.Version, Command: w.Command, Dir: w.Dir,
		Timeout: time.Duration(w.TimeoutMS) * time.Millisecond, Required: w.Required,
	}
	return nil
}

// EffectiveTimeout returns the timeout to apply when running the check.
func (c CheckSpec) EffectiveTimeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultCheckTimeout
	}
	return c.Timeout
}

// Policy is the verification contract attached to a task. TaskChecks prove the
// task's own outcome; Regression checks guard the rest of the project and are
// typically inherited from the project.
type Policy struct {
	TaskChecks []CheckSpec `json:"task_checks,omitempty"`
	Regression []CheckSpec `json:"regression,omitempty"`
}

// Validate enforces structural rules. It does not judge whether the checks
// prove anything; see docs/verification.md for that limitation.
func (p Policy) Validate() error {
	seen := map[string]bool{}
	for _, group := range [][]CheckSpec{p.TaskChecks, p.Regression} {
		for _, c := range group {
			if !checkIDPattern.MatchString(c.ID) {
				return fault.New(fault.CodeInvalidInput, "check id %q is invalid (letters, digits, '.', '_', '-'; max 64)", c.ID)
			}
			if seen[c.ID] {
				return fault.New(fault.CodeInvalidInput, "duplicate check id %q", c.ID)
			}
			seen[c.ID] = true
			if len(c.Command) == 0 || c.Command[0] == "" {
				return fault.New(fault.CodeInvalidInput, "check %q has no command", c.ID)
			}
			if c.Timeout < 0 || c.Timeout > MaxCheckTimeout {
				return fault.New(fault.CodeInvalidInput, "check %q timeout must be within (0, %s]", c.ID, MaxCheckTimeout)
			}
		}
	}
	return nil
}

// Empty reports whether the policy requires nothing at all.
func (p Policy) Empty() bool { return len(p.TaskChecks) == 0 && len(p.Regression) == 0 }

// RequiredChecks returns every check that must pass, in planning order.
func (p Policy) RequiredChecks() []CheckSpec {
	var out []CheckSpec
	for _, c := range p.TaskChecks {
		if c.Required {
			out = append(out, c)
		}
	}
	for _, c := range p.Regression {
		if c.Required {
			out = append(out, c)
		}
	}
	return out
}

// Canonical returns the deterministic JSON encoding used for storage and
// digesting. Struct field order is fixed by Go, so equal policies always
// produce byte-identical output.
func (p Policy) Canonical() ([]byte, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encode policy: %w", err)
	}
	return b, nil
}

// Digest is the policy's version: a content hash of its canonical form. Any
// change to any check produces a new digest, which is what makes evidence
// gathered under an older contract distinguishable from current evidence.
func (p Policy) Digest() string {
	b, err := p.Canonical()
	if err != nil {
		// Marshal of these plain types cannot fail; treat as programming error.
		panic(err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Parse decodes a stored canonical policy.
func Parse(b []byte) (Policy, error) {
	var p Policy
	if len(b) == 0 {
		return p, nil
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("decode policy: %w", err)
	}
	return p, nil
}

// Merge returns the effective policy for a verification run: the task's
// own checks plus the project's regression checks. The two categories are
// separate namespaces: a task check whose ID matches a regression check is
// rejected at definition time (see Collides), never allowed to shadow it.
func Merge(task Policy, projectRegression []CheckSpec) Policy {
	return Policy{
		TaskChecks: append([]CheckSpec(nil), task.TaskChecks...),
		Regression: append([]CheckSpec(nil), projectRegression...),
	}
}

// Collides returns the first task check ID that also names a regression
// check, or "".
func Collides(taskChecks, regression []CheckSpec) string {
	ids := map[string]bool{}
	for _, c := range regression {
		ids[c.ID] = true
	}
	for _, c := range taskChecks {
		if ids[c.ID] {
			return c.ID
		}
	}
	return ""
}

// Mode names what a verification run proves.
type Mode string

const (
	// ModeTask: the task's own checks, run fresh, diagnostic only.
	ModeTask Mode = "task"
	// ModeRegression: the project's regression checks, run fresh,
	// diagnostic only.
	ModeRegression Mode = "regression"
	// ModeComplete: both categories fresh on an immutable revision; the
	// only mode that can establish completion.
	ModeComplete Mode = "complete"
	// ModeCohort: both categories for every cohort member on one
	// assembled candidate, run by a verifier job.
	ModeCohort Mode = "cohort"
)

// ParseMode validates a mode from user input; "" is rejected because a
// bare `verify` has no implicit meaning.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeTask, ModeRegression, ModeComplete:
		return Mode(s), nil
	}
	return "", fault.New(fault.CodeInvalidInput, "verify needs a mode: task, regression, or complete")
}

// Subset returns the checks a mode executes.
func (p Policy) Subset(m Mode) Policy {
	switch m {
	case ModeTask:
		return Policy{TaskChecks: p.TaskChecks}
	case ModeRegression:
		return Policy{Regression: p.Regression}
	}
	return p
}

// MissingCategory reports which required category is absent for a complete
// run, or "" when both are present. Completion fails closed without both.
func (p Policy) MissingCategory() string {
	switch {
	case len(p.TaskChecks) == 0:
		return "task checks"
	case len(p.Regression) == 0:
		return "project regression checks"
	}
	return ""
}
