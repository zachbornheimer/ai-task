// Package at is a durable, harness-agnostic task engine for AI coding
// agents: an atomic task graph with stable keys and organizational groups,
// leased and fenced execution claims, append-only handoffs, and completion
// that only fresh verification can establish.
//
// The Go API is primary; the CLI is a thin adapter over the same engine.
// Project bootstrap (Open, InitProject, UpdateProject) is trusted
// configuration and lives outside the everyday Engine interface.
//
//	eng, _ := at.Open(ctx, at.Config{})
//	proj, _ := eng.InitProject(ctx, "svc", "/path/to/repo")
//	eng.Apply(ctx, at.ChangeSet{ProjectID: proj.ID, Operations: []at.Change{
//	    at.AddTask{Key: "parse", Title: "Parse tokens", TaskChecks: []at.CheckSpec{{ID: "unit", Command: []string{"go", "test", "./auth/..."}, Required: true}}},
//	    at.AddTask{Key: "reject", Title: "Reject expired access tokens", Requires: []at.Ref{"parse"}, TaskChecks: ...},
//	}})
//	s, _ := eng.Claim(ctx, at.ClaimRequest{ProjectID: proj.ID, Wait: true})
//	eng.Log(ctx, s.Token, at.LogEntry{Done: "..."})
//	res, err := eng.Verify(ctx, s.Token, at.VerifyComplete)
package at

import (
	"context"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// Engine is the everyday task interface. *Store implements it.
type Engine interface {
	Apply(ctx context.Context, set ChangeSet) (ApplyResult, error)
	List(ctx context.Context, q ListQuery) (PlanSnapshot, error)
	Show(ctx context.Context, project ProjectID, ref string, full bool) (TaskView, error)

	Claim(ctx context.Context, q ClaimRequest) (Session, error)
	Renew(ctx context.Context, token SessionToken) (time.Time, error)
	Release(ctx context.Context, token SessionToken, opts ReleaseOptions) (TaskView, error)
	Log(ctx context.Context, token SessionToken, entry LogEntry) (RecordedLog, error)

	Verify(ctx context.Context, token SessionToken, mode VerifyMode) (VerifyResult, error)
}

// Store is the concrete engine returned by Open; it also carries the
// trusted bootstrap operations (InitProject, UpdateProject, Projects,
// ResolveProject, Summary, RunPendingCohorts, Whoami, Close).
type Store = app.Engine

var _ Engine = (*Store)(nil)

// Config configures Open.
type Config = app.Config

// Open opens (creating if needed) the task store.
func Open(ctx context.Context, cfg Config) (*Store, error) { return app.Open(ctx, cfg) }

// DefaultPath returns the OS-appropriate database location.
func DefaultPath() (string, error) { return app.DefaultPath() }

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
	Kind                = task.Kind
	AcceptanceCriterion = task.AcceptanceCriterion
	Status              = task.Status
	CheckSpec           = verification.CheckSpec
	VerificationPolicy  = verification.Policy
	VerifyMode          = verification.Mode
	Project             = project.Project
	IntegrationPolicy   = project.IntegrationPolicy
)

// Execution types.
type (
	LogEntry       = execution.LogEntry
	RecordedLog    = execution.RecordedLog
	Handoff        = execution.Handoff
	Session        = app.Session
	ClaimRequest   = app.ClaimRequest
	ReleaseOptions = app.ReleaseOptions
)

// Read models.
type (
	ListQuery    = app.ListQuery
	Filter       = app.Filter
	PlanSnapshot = app.PlanSnapshot
	TaskView     = app.TaskView
	GroupView    = app.GroupView
	Summary      = app.Summary
	VerifyResult = app.VerifyResult
	Evidence     = verification.Evidence
)

// Verify modes.
const (
	VerifyTask       = verification.ModeTask
	VerifyRegression = verification.ModeRegression
	VerifyComplete   = verification.ModeComplete
)

// List filters.
const (
	ListAll      = app.FilterAll
	ListOpen     = app.FilterOpen
	ListReady    = app.FilterReady
	ListBlocked  = app.FilterBlocked
	ListArchived = app.FilterArchived
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
