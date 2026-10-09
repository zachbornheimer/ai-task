package task

// Status is the user-facing state of a task. It is never stored; it is
// derived from recorded facts (leases, dependencies, submissions, evidence)
// every time it is read, so it cannot drift from the facts.
type Status string

const (
	StatusBlocked              Status = "blocked"
	StatusAvailable            Status = "available"
	StatusInProgress           Status = "in_progress"
	StatusInterrupted          Status = "interrupted"
	StatusAwaitingVerification Status = "awaiting_verification"
	StatusVerificationFailed   Status = "verification_failed"
	StatusAwaitingIntegration  Status = "awaiting_integration"
	StatusComplete             Status = "complete"
)

// Facts are the recorded truths a status is derived from. Each field is owned
// by another domain (execution, dependency, verification); this type only
// combines them.
type Facts struct {
	// Complete: the completion fact has been recorded (all gates passed under
	// the project's integration policy).
	Complete bool
	// UnmetDependencies counts prerequisites that are not complete.
	UnmetDependencies int
	// LeaseActive: the current execution attempt holds an unexpired lease.
	LeaseActive bool
	// Interrupted: the current attempt was never finished and its lease has
	// expired. The work is recoverable by a new attempt.
	Interrupted bool
	// SubmissionPending: the latest submission has not been verified yet
	// (no run, or a run that is pending/running).
	SubmissionPending bool
	// VerificationFailed: the latest submission's latest run failed or
	// errored.
	VerificationFailed bool
	// AwaitingIntegration: the latest submission is verified but the
	// project's integration policy has not been satisfied.
	AwaitingIntegration bool
}

// Derive applies the precedence documented in docs/invariants.md:
//
//	complete > awaiting_integration > awaiting_verification > in_progress
//	> blocked > verification_failed > interrupted > available
//
// in_progress outranks blocked so that a dependency added after a take does
// not hide the active attempt; blocked outranks the recoverable states so
// that an agent is never offered work it cannot start.
func Derive(f Facts) Status {
	switch {
	case f.Complete:
		return StatusComplete
	case f.AwaitingIntegration:
		return StatusAwaitingIntegration
	case f.SubmissionPending:
		return StatusAwaitingVerification
	case f.LeaseActive:
		return StatusInProgress
	case f.UnmetDependencies > 0:
		return StatusBlocked
	case f.VerificationFailed:
		return StatusVerificationFailed
	case f.Interrupted:
		return StatusInterrupted
	default:
		return StatusAvailable
	}
}

// Takeable reports whether a new execution attempt may start.
func (s Status) Takeable() bool {
	switch s {
	case StatusAvailable, StatusInterrupted, StatusVerificationFailed:
		return true
	}
	return false
}

// TakePriority orders takeable tasks for automatic selection: resume
// interrupted work first, repair failed work next, then start fresh work.
// Lower is earlier.
func (s Status) TakePriority() int {
	switch s {
	case StatusInterrupted:
		return 0
	case StatusVerificationFailed:
		return 1
	case StatusAvailable:
		return 2
	}
	return 3
}

// ParseStatus validates a status string from user input.
func ParseStatus(s string) (Status, bool) {
	switch Status(s) {
	case StatusBlocked, StatusAvailable, StatusInProgress, StatusInterrupted,
		StatusAwaitingVerification, StatusVerificationFailed, StatusAwaitingIntegration, StatusComplete:
		return Status(s), true
	}
	return "", false
}
