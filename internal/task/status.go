package task

import "time"

// Status is the user-facing state of a task. It is never stored; it is
// derived from recorded facts every time it is read, so it cannot drift.
type Status string

const (
	StatusArchived             Status = "archived"
	StatusComplete             Status = "complete"
	StatusAwaitingIntegration  Status = "awaiting_integration"
	StatusVerifying            Status = "verifying"
	StatusAwaitingVerification Status = "awaiting_verification"
	StatusClaimed              Status = "claimed"
	StatusBlocked              Status = "blocked"
	StatusNeedsAttention       Status = "needs_attention"
	StatusVerificationFailed   Status = "verification_failed"
	StatusCooldown             Status = "cooldown"
	StatusInterrupted          Status = "interrupted"
	StatusReady                Status = "ready"
	// StatusGroup is the status of an organizational group; progress is
	// derived from its members and reported separately.
	StatusGroup Status = "group"
)

// Facts are the recorded truths a status is derived from.
type Facts struct {
	Group    bool
	Archived bool
	// Complete: the completion fact has been recorded.
	Complete bool
	// UnmetRequires counts hard prerequisites that are not complete.
	UnmetRequires int
	// LeaseActive: the current attempt holds an unexpired lease.
	LeaseActive bool
	// Interrupted: the current attempt was never ended and its lease expired.
	Interrupted bool
	// Verifying: a final verification run (or cohort job) is executing.
	Verifying bool
	// SubmissionPending: the newest submission is waiting to be judged
	// (cohort peers, or a verifier to pick it up).
	SubmissionPending bool
	// AwaitingIntegration: judged passed, integration not yet recorded.
	AwaitingIntegration bool
	// VerificationFailed: the newest final judgement failed and no claim
	// is live.
	VerificationFailed bool
	// Exhausted: failures reached the project's attempt limit.
	Exhausted bool
	// Unverifiable: the task's stored checks cannot act as a gate (no
	// required check), so no attempt could ever complete it. A planner
	// must repair the checks; it is never handed out.
	Unverifiable bool
	// WorkspaceBlocked: the task's worktree could not be prepared at the
	// last claim. Automatic selection skips it so the queue keeps moving;
	// an explicit claim retries; a planner reset clears it.
	WorkspaceBlocked bool
	// CooldownUntil, when after now, delays the next claim.
	CooldownUntil time.Time
	Now           time.Time
}

// Derive applies the precedence documented in docs/invariants.md:
//
//	archived > complete > awaiting_integration > verifying
//	> awaiting_verification > claimed > blocked > needs_attention
//	> cooldown > verification_failed > interrupted > ready
//
// Cooldown outranks verification_failed so that a task whose newest proof
// failed is not claimable while its retry cooldown runs: eligibility is
// one rule, and the SQL prefilter (next_eligible_at <= now) agrees.
func Derive(f Facts) Status {
	switch {
	case f.Archived:
		return StatusArchived
	case f.Group:
		return StatusGroup
	case f.Complete:
		return StatusComplete
	case f.AwaitingIntegration:
		return StatusAwaitingIntegration
	case f.Verifying:
		return StatusVerifying
	case f.SubmissionPending:
		return StatusAwaitingVerification
	case f.LeaseActive:
		return StatusClaimed
	case f.UnmetRequires > 0:
		return StatusBlocked
	case f.Exhausted, f.Unverifiable, f.WorkspaceBlocked:
		return StatusNeedsAttention
	case f.CooldownUntil.After(f.Now):
		return StatusCooldown
	case f.VerificationFailed:
		return StatusVerificationFailed
	case f.Interrupted:
		return StatusInterrupted
	default:
		return StatusReady
	}
}

// Claimable reports whether a new execution attempt may start now.
func (s Status) Claimable() bool {
	switch s {
	case StatusReady, StatusInterrupted, StatusVerificationFailed:
		return true
	}
	return false
}

// Active reports whether the task's outcome is being produced or judged by
// a live process or a recoverable job, i.e. a waiting worker should keep
// waiting rather than declare the project stalled.
func (s Status) Active() bool {
	switch s {
	case StatusClaimed, StatusVerifying, StatusAwaitingVerification, StatusAwaitingIntegration:
		return true
	}
	return false
}

// Open reports whether the task still needs work (not complete, not
// archived, not a group).
func (s Status) Open() bool {
	return s != StatusComplete && s != StatusArchived && s != StatusGroup
}

// Glyph is the Beads-style progress glyph: ○ not in progress, ◐ in
// progress (live claim or verification), ● complete.
func (s Status) Glyph() string {
	switch s {
	case StatusComplete:
		return "●"
	case StatusClaimed, StatusVerifying:
		return "◐"
	}
	return "○"
}

// Annotation is the bracketed text shown after a title for states the
// glyph does not express. Ready, claimed, complete and group have none.
func (s Status) Annotation() string {
	switch s {
	case StatusBlocked:
		return "[blocked]"
	case StatusAwaitingVerification:
		return "[awaiting verification]"
	case StatusVerifying:
		return "[verifying]"
	case StatusAwaitingIntegration:
		return "[awaiting integration]"
	case StatusVerificationFailed:
		return "[failed]"
	case StatusNeedsAttention:
		return "[needs attention]"
	case StatusCooldown:
		return "[cooldown]"
	case StatusInterrupted:
		return "[interrupted]"
	case StatusArchived:
		return "[archived]"
	}
	return ""
}

// ParseStatus validates a status string from user input.
func ParseStatus(s string) (Status, bool) {
	switch Status(s) {
	case StatusArchived, StatusComplete, StatusAwaitingIntegration, StatusVerifying, StatusAwaitingVerification,
		StatusClaimed, StatusBlocked, StatusNeedsAttention, StatusVerificationFailed, StatusCooldown, StatusInterrupted, StatusReady, StatusGroup:
		return Status(s), true
	}
	return "", false
}
