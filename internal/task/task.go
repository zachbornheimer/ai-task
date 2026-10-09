// Package task owns the semantic contract of a task: its stable identity, the
// outcome it promises, its constraints and acceptance criteria, and the
// verification contract bound to it. It also owns the rules that turn
// recorded facts into a user-facing status (status.go).
package task

import (
	"regexp"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// ID is an opaque task identifier: "at-" + 6 base32 characters (30 bits).
// IDs are random, never derived from content, and never reused; the store's
// UNIQUE constraint detects the rare collision and the engine redraws.
// Legacy "task-" + 16 character IDs from earlier releases still parse.
type ID string

const (
	idPrefix       = "at-"
	idChars        = 6
	legacyPrefix   = "task-"
	legacyIDLength = 16
)

// NewID draws a fresh random ID.
func NewID() ID { return ID(idPrefix + project.RandomBase32(idChars)) }

// ParseID validates the external form.
func ParseID(s string) (ID, error) {
	switch {
	case strings.HasPrefix(s, idPrefix) && project.ValidBase32(strings.TrimPrefix(s, idPrefix), idChars):
		return ID(s), nil
	case strings.HasPrefix(s, legacyPrefix) && project.ValidBase32(strings.TrimPrefix(s, legacyPrefix), legacyIDLength):
		return ID(s), nil
	}
	return "", fault.New(fault.CodeInvalidInput, "invalid task id %q (expected at-xxxxxx)", s)
}

// IsID reports whether s has the syntax of an ID (as opposed to a key).
func IsID(s string) bool { _, err := ParseID(s); return err == nil }

// Kind distinguishes executable tasks from organizational groups.
type Kind string

const (
	KindTask  Kind = "task"
	KindGroup Kind = "group"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

// ValidateKey checks a caller-supplied stable key. Keys must not look like
// IDs so that references are unambiguous.
func ValidateKey(k string) error {
	if k == "" {
		return nil
	}
	if !keyPattern.MatchString(k) || IsID(k) {
		return fault.New(fault.CodeInvalidInput, "invalid key %q (letters, digits, '.', '_', '-', '/'; max 128; not an id)", k)
	}
	return nil
}

// AcceptanceCriterion is one human-checkable statement that must hold when
// the task is done. Criteria are documentation for the agent and reviewer;
// they are not executed.
type AcceptanceCriterion struct {
	Description string `json:"description"`
}

// Spec is the input to task creation.
type Spec struct {
	ProjectID    project.ID
	Kind         Kind
	Key          string
	ParentID     ID // organizational group, optional
	Description  string
	Outcome      string
	Constraints  []string
	Acceptance   []AcceptanceCriterion
	Verification verification.Policy
	// Cohort names a coupled-verification cohort: members implement
	// independently and are judged together on one assembled candidate.
	Cohort string
}

// Task is the stored contract.
type Task struct {
	ID           ID                    `json:"id"`
	ProjectID    project.ID            `json:"project_id"`
	Kind         Kind                  `json:"kind"`
	Key          string                `json:"key,omitempty"`
	ParentID     ID                    `json:"parent_id,omitempty"`
	Description  string                `json:"title"`
	Outcome      string                `json:"outcome"`
	Constraints  []string              `json:"constraints,omitempty"`
	Acceptance   []AcceptanceCriterion `json:"acceptance,omitempty"`
	Verification verification.Policy   `json:"verification"`
	Cohort       string                `json:"cohort,omitempty"`
	ContractRev  int                   `json:"contract_rev"`
	ArchivedAt   *time.Time            `json:"archived_at,omitempty"`
	CreatedAt    time.Time             `json:"created_at"`
	UpdatedAt    time.Time             `json:"updated_at"`
}

// Archived reports whether the task has been soft-deleted.
func (t Task) Archived() bool { return t.ArchivedAt != nil }

const (
	maxDescription = 4000
	maxOutcome     = 4000
	maxConstraint  = 2000
	maxCriterion   = 2000
	maxListItems   = 100
)

// Validate enforces the structural rules of a contract and normalises
// whitespace. Semantic atomicity cannot be checked syntactically; Warnings
// reports heuristics that suggest a task may bundle several outcomes.
func (s *Spec) Validate() error {
	if s.ProjectID == "" {
		return fault.New(fault.CodeInvalidInput, "task needs a project")
	}
	if s.Kind == "" {
		s.Kind = KindTask
	}
	if s.Kind != KindTask && s.Kind != KindGroup {
		return fault.New(fault.CodeInvalidInput, "unknown kind %q", s.Kind)
	}
	if err := ValidateKey(s.Key); err != nil {
		return err
	}
	s.Description = strings.TrimSpace(s.Description)
	if s.Description == "" {
		return fault.New(fault.CodeInvalidInput, "title must not be empty")
	}
	if len(s.Description) > maxDescription {
		return fault.New(fault.CodeInvalidInput, "title must be at most %d characters", maxDescription)
	}
	s.Outcome = strings.TrimSpace(s.Outcome)
	if s.Outcome == "" {
		// One intended outcome is mandatory; a short title is usually the
		// outcome itself ("Reject expired access tokens").
		s.Outcome = s.Description
	}
	if len(s.Outcome) > maxOutcome {
		return fault.New(fault.CodeInvalidInput, "outcome must be at most %d characters", maxOutcome)
	}
	if len(s.Constraints) > maxListItems || len(s.Acceptance) > maxListItems {
		return fault.New(fault.CodeInvalidInput, "at most %d constraints and %d acceptance criteria", maxListItems, maxListItems)
	}
	cleaned := s.Constraints[:0]
	for _, c := range s.Constraints {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if len(c) > maxConstraint {
			return fault.New(fault.CodeInvalidInput, "constraint too long (max %d)", maxConstraint)
		}
		cleaned = append(cleaned, c)
	}
	s.Constraints = cleaned
	accepted := s.Acceptance[:0]
	for _, a := range s.Acceptance {
		a.Description = strings.TrimSpace(a.Description)
		if a.Description == "" {
			continue
		}
		if len(a.Description) > maxCriterion {
			return fault.New(fault.CodeInvalidInput, "acceptance criterion too long (max %d)", maxCriterion)
		}
		accepted = append(accepted, a)
	}
	s.Acceptance = accepted
	s.Cohort = strings.TrimSpace(s.Cohort)
	if err := ValidateKey(s.Cohort); err != nil {
		return fault.New(fault.CodeInvalidInput, "invalid cohort name %q", s.Cohort)
	}
	if s.Kind == KindGroup {
		if !s.Verification.Empty() || s.Cohort != "" {
			return fault.New(fault.CodeInvalidInput, "a group cannot have checks or a cohort; it is organizational only")
		}
	}
	if len(s.Verification.Regression) > 0 {
		return fault.New(fault.CodeInvalidInput, "regression checks are project-level; tasks define task checks only")
	}
	return s.Verification.Validate()
}

// Warnings returns non-blocking hints about semantic atomicity. They are
// heuristics, deliberately weak, and never gate creation.
func (s Spec) Warnings() []string {
	var w []string
	if s.Kind == KindGroup {
		return nil
	}
	lower := strings.ToLower(s.Outcome)
	for _, conj := range []string{" and ", " and also ", "; "} {
		if strings.Contains(lower, conj) {
			w = append(w, "outcome contains a conjunction; if it names two independently verifiable results, split the task")
			break
		}
	}
	if len(s.Acceptance) > 12 {
		w = append(w, "more than 12 acceptance criteria; consider whether this is one semantic outcome")
	}
	if len(s.Verification.TaskChecks) == 0 {
		w = append(w, "no task checks: `verify complete` will fail closed until checks are defined")
	}
	return w
}
