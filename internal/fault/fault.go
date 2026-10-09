// Package fault defines the stable, machine-readable error codes the engine
// and CLI expose. Every error that crosses the public API boundary carries a
// Code so that agents can branch on it without parsing prose.
package fault

import (
	"errors"
	"fmt"
)

// Code is a stable error identifier. Codes are part of the public contract and
// are documented in docs/agent-contract.md; never rename an existing code.
type Code string

const (
	// Input and lookup failures.
	CodeInvalidInput Code = "INVALID_INPUT"
	CodeNotFound     Code = "NOT_FOUND"
	CodeNoProject    Code = "NO_PROJECT"

	// Planning failures.
	CodePlanConflict Code = "PLAN_CONFLICT"
	CodeDuplicateKey Code = "DUPLICATE_KEY"

	// Dependency graph failures.
	CodeDependencyCycle     Code = "DEPENDENCY_CYCLE"
	CodeSelfDependency      Code = "SELF_DEPENDENCY"
	CodeDuplicateDependency Code = "DUPLICATE_DEPENDENCY"
	CodeCrossProject        Code = "CROSS_PROJECT_DEPENDENCY"

	// Take failures: the task exists but is not takeable right now.
	CodeTaskBlocked              Code = "TASK_BLOCKED"
	CodeTaskAlreadyTaken         Code = "ALREADY_CLAIMED"
	CodeTaskComplete             Code = "TASK_COMPLETE"
	CodeTaskAwaitingVerification Code = "AWAITING_VERIFICATION"
	CodeTaskAwaitingIntegration  Code = "INTEGRATION_PENDING"
	CodeNoAvailableTask          Code = "NO_ELIGIBLE_WORK"
	CodeDone                     Code = "DONE"
	CodeStalled                  Code = "STALLED"

	// Session authority failures.
	CodeInvalidSession    Code = "INVALID_SESSION"
	CodeLeaseExpired      Code = "LEASE_EXPIRED"
	CodeSessionSuperseded Code = "SESSION_SUPERSEDED"
	CodeSessionFinished   Code = "SESSION_FINISHED"

	// Verification and workspace failures.
	CodeVerificationFailed      Code = "VERIFICATION_FAILED"
	CodeMissingVerification     Code = "MISSING_VERIFICATION"
	CodeRevisionChanged         Code = "REVISION_CHANGED"
	CodeIntegrationFailed       Code = "INTEGRATION_FAILED"
	CodeVerificationRunning     Code = "VERIFICATION_RUNNING"
	CodeNothingToVerify         Code = "NOTHING_TO_VERIFY"
	CodeWorkspaceUnavailable    Code = "WORKSPACE_UNAVAILABLE"
	CodeWorkspaceDirty          Code = "WORKSPACE_DIRTY"
	CodeSubmissionNotIntegrated Code = "SUBMISSION_NOT_INTEGRATED"

	// Anything else: a bug, an I/O failure, a corrupt store.
	CodeInternal Code = "INTERNAL"
)

// Error is a coded error. Message is safe to show to users and agents.
type Error struct {
	Code    Code
	Message string
	Err     error // optional underlying cause
	// Details optionally carries a structured result for failures that
	// still recorded facts (e.g. VERIFICATION_FAILED carries the run).
	Details any
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// Is makes errors.Is match any error carrying the same code, so sentinel
// values such as ErrDone compare by code rather than by pointer.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// New builds a coded error.
func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap attaches a code and message to an underlying error.
func Wrap(err error, code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Err: err}
}

// CodeOf returns the code carried by err, or CodeInternal when err carries
// none. A nil error has no code and returns "".
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}

// Is reports whether err carries the given code.
func Is(err error, code Code) bool { return CodeOf(err) == code }
