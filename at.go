// Package at is a durable, harness-agnostic task engine for AI coding
// agents: an atomic task graph with stable keys and organizational groups,
// leased and fenced execution claims, append-only handoffs, and completion
// that only fresh verification can establish.
//
// The Go API is primary; the CLI is a thin adapter over the same engine.
// docs/contract.md is the one statement of it. Project bootstrap (Open,
// InitProject, UpdateProject) is trusted configuration and lives outside
// the everyday Engine interface.
//
//	eng, _ := at.Open(ctx, at.Config{})
//	proj, _ := eng.InitProject(ctx, "svc", "/path/to/repo")
//	eng.Apply(ctx, at.ChangeSet{ProjectID: proj.ID, Operations: []at.Change{
//	    at.AddTask{Key: "parse", Title: "Parse tokens", Proof: &at.ProofSpec{Preset: "go_test", Subject: "./auth"}, Regression: ...},
//	}})
//	s, _ := eng.Claim(ctx, at.ClaimRequest{ProjectID: proj.ID, Wait: true})
//	eng.Log(ctx, s.Token, at.LogEntry{Done: "..."})
//	res, err := eng.Verify(ctx, at.VerifyRequest{TaskID: s.Task.ID, Token: s.Token, Mode: at.VerifyComplete})
package at

import (
	"context"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/proof"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// Engine is the everyday task interface. *Store implements it.
type Engine interface {
	// Planning
	Apply(ctx context.Context, set ChangeSet) (ApplyResult, error)
	List(ctx context.Context, q ListQuery) (PlanSnapshot, error)
	Show(ctx context.Context, id TaskID) (TaskView, error)

	// Execution
	Claim(ctx context.Context, q ClaimRequest) (Session, error)
	Renew(ctx context.Context, token SessionToken) (time.Time, error)
	Release(ctx context.Context, q ReleaseRequest) error
	Log(ctx context.Context, token SessionToken, entry LogEntry) error

	// Verification, completion, action retries
	Verify(ctx context.Context, q VerifyRequest) (VerifyResult, error)
}

// VerifyRequest names what to verify and under which session. A group has
// no session: its Token is empty and Mode is VerifyComplete.
type VerifyRequest struct {
	TaskID   TaskID
	Token    SessionToken
	Mode     VerifyMode
	Reverify bool
}

// Store is the engine returned by Open. It implements Engine and also
// carries the trusted bootstrap operations (InitProject, UpdateProject,
// Projects, ResolveProject, Summary, Close).
type Store struct{ *app.Engine }

var _ Engine = (*Store)(nil)

// Config configures Open.
type Config = app.Config

// Open opens (creating if needed) the task store.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	e, err := app.Open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Store{e}, nil
}

// DefaultPath returns the OS-appropriate database location.
func DefaultPath() (string, error) { return app.DefaultPath() }

// Show returns the task with its full history, run results and actions.
func (s *Store) Show(ctx context.Context, id TaskID) (TaskView, error) {
	return s.Engine.Show(ctx, "", string(id), true)
}

// Log appends a handoff entry under the session.
func (s *Store) Log(ctx context.Context, token SessionToken, entry LogEntry) error {
	_, err := s.Engine.Log(ctx, token, entry)
	return err
}

// Verify runs the requested verification. Proof and regression runs are
// diagnostic; complete completes the task and runs its on_complete
// actions, and on a completed task retries the ones still owed. With
// Reverify it appends evidence against the recorded revision.
func (s *Store) Verify(ctx context.Context, q VerifyRequest) (VerifyResult, error) {
	if q.Token == "" {
		return s.verifyGroup(ctx, q)
	}
	if q.TaskID != "" {
		held, err := s.Whoami(ctx, q.Token)
		if err == nil && held.ID != q.TaskID {
			return VerifyResult{}, fault.New(fault.CodeInvalidSession, "session token belongs to %s, not %s", held.ID, q.TaskID)
		}
	}
	if q.Reverify {
		if q.Mode != VerifyComplete {
			return VerifyResult{}, fault.New(fault.CodeInvalidInput, "Reverify applies to %s only", VerifyComplete)
		}
		return s.Reverify(ctx, q.Token)
	}
	return s.Engine.Verify(ctx, q.Token, q.Mode)
}

func (s *Store) verifyGroup(ctx context.Context, q VerifyRequest) (VerifyResult, error) {
	if q.TaskID == "" || q.Mode != VerifyComplete || q.Reverify {
		return VerifyResult{}, fault.New(fault.CodeInvalidSession, "a session token is required, except to complete a group")
	}
	v, err := s.Engine.Show(ctx, "", string(q.TaskID), false)
	if err != nil {
		return VerifyResult{}, err
	}
	if v.Kind != KindGroup {
		return VerifyResult{}, fault.New(fault.CodeInvalidSession, "%s is a task: a session token is required", q.TaskID)
	}
	proj, err := s.ResolveProject(ctx, v.Project, "")
	if err != nil {
		return VerifyResult{}, err
	}
	return s.VerifyGroup(ctx, proj.ID, string(q.TaskID))
}

// Identity types.
type (
	TaskID       = task.ID
	ProjectID    = project.ID
	SessionToken = execution.Token
)

// Planning types.
type (
	ChangeSet   = plan.ChangeSet
	Change      = plan.Change
	AddGroup    = plan.AddGroup
	AddTask     = plan.AddTask
	UpdateTask  = plan.UpdateTask
	ArchiveTask = plan.ArchiveTask
	Ref         = plan.Ref
	ApplyResult = plan.Result
)

// Patch distinguishes "leave unchanged" from "replace with".
type Patch[T any] = plan.Patch[T]

// Replace builds a Patch that replaces a list.
func Replace[T any](v []T) Patch[T] { return plan.Replace(v) }

// Contract types.
type (
	Task                = task.Task
	TaskKind            = task.Kind
	AcceptanceCriterion = task.AcceptanceCriterion
	Status              = task.Status
	VerifyMode          = verification.Mode
	Project             = project.Project
)

// Proof types.
type (
	ProofSpec    = proof.ProofSpec
	ActionSpec   = proof.ActionSpec
	RunResult    = proof.RunResult
	ActionResult = proof.ActionResult
	Size         = proof.Size
	CheckType    = proof.CheckType
	ActionStatus = proof.ActionStatus
)

// Execution types.
type (
	LogEntry       = execution.LogEntry
	Handoff        = execution.Handoff
	Session        = app.Session
	ClaimRequest   = app.ClaimRequest
	ReleaseRequest = app.ReleaseRequest
)

// Read models.
type (
	ListQuery    = app.ListQuery
	ListFilter   = app.Filter
	PlanSnapshot = app.PlanSnapshot
	TaskView     = app.TaskView
	GroupView    = app.GroupView
	Summary      = app.Summary
	VerifyResult = app.VerifyResult
	Evidence     = verification.Evidence
)

// Verify modes.
const (
	VerifyProof      = verification.ModeProof
	VerifyRegression = verification.ModeRegression
	VerifyComplete   = verification.ModeComplete
)

// Task kinds.
const (
	KindTask  = task.KindTask
	KindGroup = task.KindGroup
)

// Regression sizes.
const (
	SizeSmall   = proof.SizeSmall
	SizeMedium  = proof.SizeMedium
	SizeLarge   = proof.SizeLarge
	SizeEternal = proof.SizeEternal
)

// Run types.
const (
	CheckProof      = proof.CheckProof
	CheckRegression = proof.CheckRegression
	CheckOnComplete = proof.CheckOnComplete
)

// Action states.
const (
	ActionPending   = proof.ActionPending
	ActionRunning   = proof.ActionRunning
	ActionDetached  = proof.ActionDetached
	ActionSucceeded = proof.ActionSucceeded
	ActionFailed    = proof.ActionFailed
)

// List filters.
const (
	FilterAll      = app.FilterAll
	FilterOpen     = app.FilterOpen
	FilterReady    = app.FilterReady
	FilterBlocked  = app.FilterBlocked
	FilterDraft    = app.FilterDraft
	FilterComplete = app.FilterComplete
	FilterArchived = app.FilterArchived
)

// Typed outcomes of a waiting Claim; test with errors.Is.
var (
	ErrDone    = app.ErrDone
	ErrStalled = app.ErrStalled
)

// Errors.
type (
	Code  = fault.Code
	Error = fault.Error
)

// Glyph is the progress glyph for a status: ○ never started, ◐ started
// but not complete, ● complete.
func Glyph(s Status, started bool) string { return task.Glyph(s, started) }

// ErrorCode returns the stable code carried by err ("" for nil).
func ErrorCode(err error) Code { return fault.CodeOf(err) }

// ParseTaskID validates an external task ID.
func ParseTaskID(s string) (TaskID, error) { return task.ParseID(s) }

// ParseSessionToken validates an external session token.
func ParseSessionToken(s string) (SessionToken, error) { return execution.ParseToken(s) }
