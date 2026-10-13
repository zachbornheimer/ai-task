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
	// CodeInvalidInput: a malformed argument, reference, spec, or graph.
	CodeInvalidInput Code = "INVALID_INPUT"
	// CodeNotFound: the named task, project, or session does not exist.
	CodeNotFound Code = "NOT_FOUND"
	// CodeConflict: the request clashes with current state (a plan edit,
	// a task already claimed or complete, a verification in flight).
	CodeConflict Code = "CONFLICT"
	// CodeInvalidSession: the session token is unknown, expired,
	// superseded, or finished.
	CodeInvalidSession Code = "INVALID_SESSION"
	// CodeNotReady: the operation cannot run right now (blocked or
	// awaiting work, unusable workspace, infrastructure failure).
	CodeNotReady Code = "NOT_READY"
	// CodeBaseConflict: the target branch moved or the work cannot be
	// integrated onto it.
	CodeBaseConflict Code = "BASE_CONFLICT"
	// CodeCheckFailed: a required check failed, timed out, or was
	// unrunnable.
	CodeCheckFailed Code = "CHECK_FAILED"
	// CodeIdempotencyConflict: an idempotency key was reused with a
	// different request.
	CodeIdempotencyConflict Code = "IDEMPOTENCY_CONFLICT"
	// CodeDone: claim --wait on a board with nothing left to do.
	CodeDone Code = "DONE"
	// CodeStalled: claim --wait with only tasks that cannot proceed.
	CodeStalled Code = "STALLED"
)

// Error is a coded error. Message is safe to show to users and agents.
type Error struct {
	Code    Code
	Message string
	Err     error // optional underlying cause
	// Details optionally carries a structured result for failures that
	// still recorded facts (e.g. CHECK_FAILED carries the run).
	Details any
	// internal marks an infrastructure failure (I/O, store, tooling) as
	// opposed to a domain refusal; it travels as NOT_READY on the wire.
	internal bool
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

// NewInternal builds an infrastructure failure: wire code NOT_READY,
// distinguishable from domain refusals through IsInternal.
func NewInternal(format string, args ...any) *Error {
	return &Error{Code: CodeNotReady, Message: fmt.Sprintf(format, args...), internal: true}
}

// WrapInternal attaches an infrastructure failure to an underlying error.
func WrapInternal(err error, format string, args ...any) *Error {
	return &Error{Code: CodeNotReady, Message: fmt.Sprintf(format, args...), Err: err, internal: true}
}

// IsInternal reports whether err is an infrastructure failure: built by
// NewInternal/WrapInternal, or not a fault error at all.
func IsInternal(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.internal
	}
	return err != nil
}

// CodeOf returns the code carried by err, or CodeNotReady when err carries
// none. A nil error has no code and returns "".
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeNotReady
}

// Is reports whether err carries the given code.
func Is(err error, code Code) bool { return CodeOf(err) == code }

// MessageOf returns the message of a coded error without its code, or
// err.Error() for any other error.
func MessageOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Message
	}
	if err == nil {
		return ""
	}
	return err.Error()
}
