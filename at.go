// Package at is a durable, harness-agnostic task engine: an atomic task
// graph, leased claims, and completion that only fresh verification can
// establish.
//
// Project bootstrap lives outside this eight-method Engine.
package at

import (
	"context"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// Engine is the everyday task interface.
type Engine interface {
	Apply(ctx context.Context, set ChangeSet) (ApplyResult, error)
	List(ctx context.Context, q ListQuery) (PlanSnapshot, error)
	Show(ctx context.Context, id TaskID) (TaskView, error)

	Claim(ctx context.Context, q ClaimRequest) (Session, error)
	Renew(ctx context.Context, token SessionToken) (time.Time, error)
	Release(ctx context.Context, token SessionToken, note string) error
	Log(ctx context.Context, token SessionToken, entry LogEntry) error

	Verify(ctx context.Context, req VerifyRequest) (VerifyResult, error)
}

type (
	TaskID       = task.ID
	ProjectID    = project.ID
	SessionToken = execution.Token
	ChangeSet    = plan.ChangeSet
	Change       = plan.Change
	ApplyResult  = plan.Result
	CheckSpec    = verification.CheckSpec
	VerifyMode   = verification.Mode
	LogEntry     = execution.LogEntry
	Handoff      = execution.Handoff
	Session      = app.Session
	ClaimRequest = app.ClaimRequest
	ListQuery    = app.ListQuery
	ListFilter   = app.Filter
	PlanSnapshot = app.PlanSnapshot
	TaskView     = app.TaskView
	VerifyResult = app.VerifyResult
	TaskKind     = task.Kind
)

// TaskDefinition is the planner-facing shape of a task at creation.
type TaskDefinition struct {
	Key         string
	Title       string
	Parent      TaskID
	Outcome     string
	Constraints []string
	Acceptance  []string
	Requires    []TaskID
	Cohort      string
	Checks      []CheckSpec
	OnComplete  []ActionSpec
}

// VerifyRequest selects a verification mode for a claimed (or completed) task.
type VerifyRequest struct {
	TaskID   TaskID
	Token    SessionToken
	Mode     VerifyMode
	Reverify bool
}

type (
	ActionID     string
	ActionKind   string
	ActionStatus string
)

type ActionSpec struct {
	Kind    ActionKind
	Builtin string
	Command []string
	Target  string
}

type ActionResult struct {
	ID       ActionID
	Kind     ActionKind
	Status   ActionStatus
	Attempts int
	ExitCode *int
	Stdout   string
	Stderr   string
	Error    string
	Revision string
}

const (
	VerifyTask       = verification.ModeTask
	VerifyRegression = verification.ModeRegression
	VerifyComplete   = verification.ModeComplete
)

const (
	KindTask  TaskKind = task.KindTask
	KindGroup TaskKind = task.KindGroup
)

const (
	ActionBuiltin ActionKind = "builtin"
	ActionCommand ActionKind = "command"
)

const (
	ActionPending   ActionStatus = "pending"
	ActionRunning   ActionStatus = "running"
	ActionSucceeded ActionStatus = "succeeded"
	ActionFailed    ActionStatus = "failed"
)

const (
	FilterAll      ListFilter = app.FilterAll
	FilterOpen     ListFilter = app.FilterOpen
	FilterReady    ListFilter = app.FilterReady
	FilterBlocked  ListFilter = app.FilterBlocked
	FilterComplete ListFilter = app.FilterComplete
	FilterArchived ListFilter = app.FilterArchived
)

type engine struct{ inner *app.Engine }

func (e *engine) Apply(ctx context.Context, set ChangeSet) (ApplyResult, error) {
	return e.inner.Apply(ctx, set)
}

func (e *engine) List(ctx context.Context, q ListQuery) (PlanSnapshot, error) {
	return e.inner.List(ctx, q)
}

func (e *engine) Show(ctx context.Context, id TaskID) (TaskView, error) {
	return e.inner.Show(ctx, "", string(id), false)
}

func (e *engine) Claim(ctx context.Context, q ClaimRequest) (Session, error) {
	return e.inner.Claim(ctx, q)
}

func (e *engine) Renew(ctx context.Context, token SessionToken) (time.Time, error) {
	return e.inner.Renew(ctx, token)
}

func (e *engine) Release(ctx context.Context, token SessionToken, note string) error {
	_, err := e.inner.Release(ctx, token, app.ReleaseOptions{Note: note})
	return err
}

func (e *engine) Log(ctx context.Context, token SessionToken, entry LogEntry) error {
	_, err := e.inner.Log(ctx, token, entry)
	return err
}

func (e *engine) Verify(ctx context.Context, req VerifyRequest) (VerifyResult, error) {
	return e.inner.Verify(ctx, req.Token, req.Mode)
}

var _ Engine = (*engine)(nil)
