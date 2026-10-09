// Package tasks is a durable task engine for AI coding agents: tasks with
// explicit contracts, a dependency DAG that controls eligibility, leased
// execution sessions with fenced authority, append-only structured handoff
// logs, and submission/verification facts from which status is derived.
//
// The package re-exports the internal domain types by alias so there is one
// definition of each concept; see docs/architecture.md for ownership.
//
// Typical embedding:
//
//	eng, err := tasks.Open(ctx, tasks.Config{})
//	proj, err := eng.InitProject(ctx, "my-service", "/path/to/repo")
//	t, warnings, err := eng.Add(ctx, tasks.TaskSpec{ProjectID: proj.ID, Description: "Reject expired access tokens"})
//	sess, err := eng.Take(ctx, tasks.TakeRequest{Project: proj.ID})
//	_, err = eng.Log(ctx, sess.Token, tasks.LogEntry{Done: "..."})
//	res, err := eng.Finish(ctx, sess.Token)
package tasks

import (
	"context"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/dependency"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// Engine is the single entry point; see the methods on app.Engine.
type Engine = app.Engine

// Config configures Open.
type Config = app.Config

// Open opens (creating if needed) the task store and returns an Engine.
func Open(ctx context.Context, cfg Config) (*Engine, error) { return app.Open(ctx, cfg) }

// DefaultPath returns the OS-appropriate database location.
func DefaultPath() (string, error) { return app.DefaultPath() }

// Identity types.
type (
	TaskID       = task.ID
	ProjectID    = project.ID
	SessionToken = execution.Token
)

// Contract types.
type (
	TaskSpec            = task.Spec
	Task                = task.Task
	AcceptanceCriterion = task.AcceptanceCriterion
	Status              = task.Status
	VerificationPolicy  = verification.Policy
	CheckSpec           = verification.CheckSpec
	Project             = project.Project
	IntegrationPolicy   = project.IntegrationPolicy
)

// Execution types.
type (
	LogEntry      = execution.LogEntry
	RecordedLog   = execution.RecordedLog
	Handoff       = execution.Handoff
	Session       = app.Session
	TakeRequest   = app.TakeRequest
	FinishOptions = app.FinishOptions
	VerifyOptions = app.VerifyOptions
)

// Read models.
type (
	TaskView         = app.TaskView
	TaskSummary      = app.TaskSummary
	SubmissionResult = app.SubmissionResult
	VerifyResult     = app.VerifyResult
	EvidencePage     = app.EvidencePage
	RunView          = app.RunView
	Evidence         = verification.Evidence
	HistoryPage      = app.HistoryPage
	Summary          = app.Summary
	ListFilter       = app.ListFilter
	DependencyGraph  = dependency.Graph
	Edge             = dependency.Edge
)

// Errors.
type (
	// Code is a stable error code; see docs/agent-contract.md.
	Code = fault.Code
	// Error is the coded error type returned by every Engine method.
	Error = fault.Error
)

// ErrorCode returns the stable code carried by err ("" for nil).
func ErrorCode(err error) Code { return fault.CodeOf(err) }

// ParseTaskID validates an external task ID.
// Takeable statuses are listed by Engine.Takeable; see docs/invariants.md.

func ParseTaskID(s string) (TaskID, error) { return task.ParseID(s) }

// ParseSessionToken validates an external session token.
func ParseSessionToken(s string) (SessionToken, error) { return execution.ParseToken(s) }

// Status values.
const (
	StatusBlocked              = task.StatusBlocked
	StatusAvailable            = task.StatusAvailable
	StatusInProgress           = task.StatusInProgress
	StatusInterrupted          = task.StatusInterrupted
	StatusAwaitingVerification = task.StatusAwaitingVerification
	StatusVerificationFailed   = task.StatusVerificationFailed
	StatusAwaitingIntegration  = task.StatusAwaitingIntegration
	StatusComplete             = task.StatusComplete
)
