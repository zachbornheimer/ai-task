// Package task owns the semantic contract of a task: its stable identity, the
// outcome it promises, its constraints and acceptance criteria, and the
// verification contract bound to it. It also owns the rules that turn
// recorded facts into a user-facing status (status.go).
package task

import (
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// ID is an opaque task identifier of the form "task-" + 16 base32 chars.
// It never changes after creation and is never reused.
type ID string

const idPrefix = "task-"

// NewID draws a fresh random ID (80 bits of entropy).
func NewID() ID { return ID(idPrefix + project.RandomBase32(16)) }

// ParseID validates the external form.
func ParseID(s string) (ID, error) {
	if !strings.HasPrefix(s, idPrefix) || !project.ValidBase32(strings.TrimPrefix(s, idPrefix), 16) {
		return "", fault.New(fault.CodeInvalidInput, "invalid task id %q (expected task-xxxxxxxxxxxxxxxx)", s)
	}
	return ID(s), nil
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
	Description  string
	Outcome      string
	Constraints  []string
	Acceptance   []AcceptanceCriterion
	Verification verification.Policy
}

// Task is the stored contract.
type Task struct {
	ID           ID                    `json:"id"`
	ProjectID    project.ID            `json:"project_id"`
	Description  string                `json:"description"`
	Outcome      string                `json:"outcome"`
	Constraints  []string              `json:"constraints,omitempty"`
	Acceptance   []AcceptanceCriterion `json:"acceptance,omitempty"`
	Verification verification.Policy   `json:"verification"`
	CreatedAt    time.Time             `json:"created_at"`
	UpdatedAt    time.Time             `json:"updated_at"`
}

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
	s.Description = strings.TrimSpace(s.Description)
	if s.Description == "" {
		return fault.New(fault.CodeInvalidInput, "task description must not be empty")
	}
	if len(s.Description) > maxDescription {
		return fault.New(fault.CodeInvalidInput, "task description must be at most %d characters", maxDescription)
	}
	s.Outcome = strings.TrimSpace(s.Outcome)
	if s.Outcome == "" {
		// One intended outcome is mandatory; a short description is usually
		// the outcome itself ("Reject expired access tokens").
		s.Outcome = s.Description
	}
	if len(s.Outcome) > maxOutcome {
		return fault.New(fault.CodeInvalidInput, "task outcome must be at most %d characters", maxOutcome)
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
	return s.Verification.Validate()
}

// Warnings returns non-blocking hints about semantic atomicity. They are
// heuristics, deliberately weak, and never gate creation.
func (s Spec) Warnings() []string {
	var w []string
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
	return w
}
