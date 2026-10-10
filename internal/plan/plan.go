// Package plan owns the planning contract: the closed set of changes a
// ChangeSet may contain, reference resolution rules, and patch semantics.
// Applying a ChangeSet is the application's job (app.Apply); this package
// decides what a change means and what is malformed before any store is
// touched.
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// Ref names a task in a ChangeSet: a task ID ("at-…") or a stable key,
// including the key of a task added earlier in the same ChangeSet.
type Ref string

// IsID reports whether the ref is an ID rather than a key.
func (r Ref) IsID() bool { return task.IsID(string(r)) }

// Change is one operation in a ChangeSet. The set is closed: AddGroup,
// AddTask, UpdateTask, ArchiveTask.
type Change interface{ change() }

// AddGroup creates a group. With TaskChecks it is an epic with its own
// verification, run on the target branch once every member is complete.
type AddGroup struct {
	Key        string
	Title      string
	Parent     Ref // optional enclosing group
	TaskChecks []verification.CheckSpec
	Size       string
}

// AddTask creates an executable task.
type AddTask struct {
	Key         string
	Title       string
	Outcome     string
	Parent      Ref // optional group
	Constraints []string
	Acceptance  []string
	Requires    []Ref
	Blocks      []Ref // existing tasks that will require the new one
	Cohort      string
	TaskChecks  []verification.CheckSpec
	// Pins are paths the task's branch must leave untouched.
	Pins []string
	// Size: small (default), medium or large; caps the task checks' time.
	Size string
}

// UpdateTask patches a task or group. nil means leave unchanged; a pointer
// to an empty value clears. List fields use Patch to distinguish unchanged
// from replaced.
type UpdateTask struct {
	Target         Ref
	Title          *string
	Outcome        *string
	Parent         *Ref // pointer to "" clears the parent
	Constraints    Patch[string]
	Acceptance     Patch[string]
	Cohort         *string
	TaskChecks     Patch[verification.CheckSpec]
	Pins           Patch[string]
	Size           *string
	Requires       Patch[Ref] // replace the whole prerequisite set
	AddRequires    []Ref
	RemoveRequires []Ref
	// ResetAttempts clears failure bookkeeping so an exhausted task can be
	// claimed again (planner decision).
	ResetAttempts bool
	// WithdrawSubmission withdraws a cohort member's submission that is
	// waiting for its peers, so its contract can change (planner
	// decision; the member becomes claimable and must resubmit).
	WithdrawSubmission bool
}

// ArchiveTask soft-deletes a task or group. Reason is mandatory: removing a
// step from a plan is never silent.
type ArchiveTask struct {
	Target Ref
	Reason string
}

// Patch carries a list replacement: Set false means unchanged.
type Patch[T any] struct {
	Set   bool
	Value []T
}

// Replace builds a Patch that replaces the list.
func Replace[T any](v []T) Patch[T] { return Patch[T]{Set: true, Value: v} }

func (AddGroup) change()    {}
func (AddTask) change()     {}
func (UpdateTask) change()  {}
func (ArchiveTask) change() {}

// ChangeSet is one atomic revision of a project's plan.
type ChangeSet struct {
	ProjectID project.ID
	// ExpectedPlanRev, when non-zero, must equal the project's current plan
	// revision or the set is rejected with PLAN_CONFLICT.
	ExpectedPlanRev uint64
	// IdempotencyKey, when set, makes a replay return the stored result
	// instead of applying again.
	IdempotencyKey string
	// Session optionally authorises edits that touch a claimed task's
	// eligibility (a discovered blocker added by the agent holding it).
	Session string
	// Planner asserts planning authority for edits to claimed tasks. It is
	// a convention for the local trust model, not a security boundary.
	Planner    bool
	Operations []Change
}

// Digest is a content hash of the operations (kind, order and every
// field, prerequisites and blockers included) so an idempotency key can be
// bound to the exact request it recorded. Authority fields (Session,
// Planner) and ExpectedPlanRev are not part of the request's content.
func (cs ChangeSet) Digest() string {
	type tagged struct {
		Kind string `json:"kind"`
		Op   Change `json:"op"`
	}
	ops := make([]tagged, 0, len(cs.Operations))
	for _, op := range cs.Operations {
		kind := ""
		switch op.(type) {
		case AddGroup:
			kind = "add_group"
		case AddTask:
			kind = "add_task"
		case UpdateTask:
			kind = "update_task"
		case ArchiveTask:
			kind = "archive_task"
		}
		ops = append(ops, tagged{Kind: kind, Op: op})
	}
	b, err := json.Marshal(struct {
		Project project.ID `json:"project"`
		Ops     []tagged   `json:"ops"`
	}{cs.ProjectID, ops})
	if err != nil {
		panic(err) // plain values; cannot fail
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Validate checks each change structurally. Reference existence and graph
// rules are checked by Apply against the store.
func (cs ChangeSet) Validate() error {
	if cs.ProjectID == "" {
		return fault.New(fault.CodeInvalidInput, "change set needs a project")
	}
	if len(cs.Operations) == 0 {
		return fault.New(fault.CodeInvalidInput, "change set has no operations")
	}
	keys := map[string]bool{}
	for i, op := range cs.Operations {
		switch c := op.(type) {
		case AddGroup:
			if err := task.ValidateKey(c.Key); err != nil {
				return err
			}
			if strings.TrimSpace(c.Title) == "" {
				return fault.New(fault.CodeInvalidInput, "operation %d: group title must not be empty", i)
			}
			if c.Key != "" {
				if keys[c.Key] {
					return fault.New(fault.CodeDuplicateKey, "key %q appears twice in the change set", c.Key)
				}
				keys[c.Key] = true
			}
		case AddTask:
			if err := task.ValidateKey(c.Key); err != nil {
				return err
			}
			if strings.TrimSpace(c.Title) == "" {
				return fault.New(fault.CodeInvalidInput, "operation %d: task title must not be empty", i)
			}
			if c.Key != "" {
				if keys[c.Key] {
					return fault.New(fault.CodeDuplicateKey, "key %q appears twice in the change set", c.Key)
				}
				keys[c.Key] = true
			}
			for _, r := range append(append([]Ref{}, c.Requires...), c.Blocks...) {
				if r == "" {
					return fault.New(fault.CodeInvalidInput, "operation %d: empty reference", i)
				}
			}
		case UpdateTask:
			if c.Target == "" {
				return fault.New(fault.CodeInvalidInput, "operation %d: update needs a target", i)
			}
			if c.Title != nil && strings.TrimSpace(*c.Title) == "" {
				return fault.New(fault.CodeInvalidInput, "operation %d: title cannot be cleared", i)
			}
		case ArchiveTask:
			if c.Target == "" {
				return fault.New(fault.CodeInvalidInput, "operation %d: archive needs a target", i)
			}
			if strings.TrimSpace(c.Reason) == "" {
				return fault.New(fault.CodeInvalidInput, "operation %d: archive needs a reason (--reason)", i)
			}
		default:
			return fault.New(fault.CodeInvalidInput, "operation %d: unknown change type", i)
		}
	}
	return nil
}

// Result is what Apply reports.
type Result struct {
	PlanRev  uint64             `json:"plan_rev"`
	Changed  int                `json:"changed"`
	Replayed bool               `json:"replayed,omitempty"`
	Created  map[string]task.ID `json:"created,omitempty"` // key or title -> id
	Updated  []task.ID          `json:"updated,omitempty"`
	Archived []task.ID          `json:"archived,omitempty"`
	// Warnings are non-blocking atomicity hints keyed like Created.
	Warnings map[string][]string `json:"warnings,omitempty"`
}
