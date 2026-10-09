package app

import "github.com/zachbornheimer/ai-task/internal/fault"

// failureClass says what a refused or failed completion means for the
// task's retry budget.
type failureClass int

const (
	// classAttempt: the attempt genuinely failed its proof (a required
	// check failed) or gave up (release --failed). Counts toward
	// max_attempts, at most once per attempt.
	classAttempt failureClass = iota
	// classEnvironment: the environment, not the work, stopped the run:
	// snapshot or worktree problems, lock contention, a dirty or moving
	// target, a merge the agent must resolve. Recorded as last_error;
	// never a strike.
	classEnvironment
	// classConflict: authority or contract changed under the run (session
	// superseded or expired, planner edit, new prerequisite, archive).
	// The result is rejected and recorded; never a strike.
	classConflict
)

func classify(code fault.Code) failureClass {
	switch code {
	case fault.CodeVerificationFailed:
		return classAttempt
	case fault.CodeSessionSuperseded, fault.CodeSessionFinished, fault.CodeLeaseExpired, fault.CodeInvalidSession,
		fault.CodePlanConflict, fault.CodeTaskBlocked, fault.CodeMissingVerification:
		return classConflict
	}
	return classEnvironment
}
