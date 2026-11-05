// Package execution owns temporary write authority over a task: execution
// attempts, their leases, the session tokens that prove authority, and the
// structured append-only log that carries work across attempts.
package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// Token is the secret that proves authority over one execution attempt. It is
// returned exactly once, by Take, and only its SHA-256 digest is stored.
type Token string

const tokenPrefix = "sess-"

// tokenChars * 5 bits = 160 bits of entropy.
const tokenChars = 32

// NewToken draws a fresh unguessable token.
func NewToken() Token { return Token(tokenPrefix + project.RandomBase32(tokenChars)) }

// ParseToken validates the external form without touching the store, so a
// malformed token is rejected before any query runs.
func ParseToken(s string) (Token, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, tokenPrefix) || !project.ValidBase32(strings.TrimPrefix(s, tokenPrefix), tokenChars) {
		return "", fault.New(fault.CodeInvalidSession, "malformed session token")
	}
	return Token(s), nil
}

// Digest is the stored form of a token.
func (t Token) Digest() string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// DefaultLease is the maximum silence before the engine assumes the agent
// is dead: every session-authenticated command renews it, commits renew it
// through the post-commit hook, and verification heart-beats itself, so it
// only has to cover a normal quiet stretch. Expiry is cheap to recover from
// (the next attempt reuses the task worktree), a stuck task is not.
const DefaultLease = 30 * time.Minute

// ClaimWindow is how long an attempt that has not stored its worktree yet
// holds the task. A claim records its attempt, builds the worktree, and stores
// the token in that worktree last, so a process killed in between leaves a
// claim nobody can work on and nobody holds a token to release: this window is
// the whole recovery time. It matches the caller-side bound on one claim (the
// start_atask_task capability gives up on a claim after five minutes), so a
// claim that outlives it has already failed its caller.
const ClaimWindow = 5 * time.Minute

// ClaimExpiry is the expiry an attempt is recorded with: at most the claim
// window, until its worktree exists and the claim can be worked on. The claim
// grants itself the attempt's whole lease when it stores the worktree.
func ClaimExpiry(now time.Time, lease time.Duration) time.Time {
	if lease <= 0 || lease > ClaimWindow {
		lease = ClaimWindow
	}
	return now.Add(lease)
}

// HeartbeatInterval is how often a long operation renews its lease.
const HeartbeatInterval = 5 * time.Minute

// EndReason records why an attempt stopped holding authority.
type EndReason string

const (
	// EndFinished: the attempt submitted its implementation.
	EndFinished EndReason = "finished"
	// EndSuperseded: a later attempt took the task after this one's lease
	// expired (or after a verification failure).
	EndSuperseded EndReason = "superseded"
	// EndReleased: the attempt gave the task back without submitting,
	// typically because it discovered a blocker. The handoff is preserved
	// and the task is immediately takeable again (or blocked, if a new
	// prerequisite was added).
	EndReleased EndReason = "released"
	// EndExpired: the attempt's lease ran out and nothing took the task
	// again; a reconcile closed it.
	EndExpired EndReason = "expired"
)

// Attempt is one execution attempt of a task. Seq is the fencing generation:
// attempts of one task are numbered 1, 2, 3, ... and only the attempt whose
// Seq equals the task's current generation can hold authority.
type Attempt struct {
	ID             int64
	TaskID         task.ID
	Seq            int
	StartedAt      time.Time
	LeaseExpiresAt time.Time
	// Lease is the duration granted at claim; renewals extend by it.
	Lease     time.Duration
	EndedAt   *time.Time
	EndReason EndReason
	// WorkspacePath/WorkspaceBranch name the attempt's private Git
	// worktree when the project is a repository.
	WorkspacePath   string
	WorkspaceBranch string
}

// RenewedExpiry is the expiry a renewal at now grants: never earlier than
// the current expiry (proof of life never shortens authority), and the
// attempt's own lease length ahead of now otherwise.
func (a Attempt) RenewedExpiry(now time.Time) time.Time {
	lease := a.Lease
	if lease <= 0 {
		lease = DefaultLease
	}
	next := now.Add(lease)
	if next.Before(a.LeaseExpiresAt) {
		return a.LeaseExpiresAt
	}
	return next
}

// LeaseActive reports whether the attempt currently holds authority.
func (a Attempt) LeaseActive(now time.Time) bool {
	return a.EndedAt == nil && now.Before(a.LeaseExpiresAt)
}

// Interrupted reports whether the attempt stopped without finishing: its
// lease ran out while it was still current, whether or not a reconcile has
// since recorded that (EndExpired) or the row is still open.
func (a Attempt) Interrupted(now time.Time) bool {
	return a.EndReason == EndExpired || a.EndedAt == nil && !now.Before(a.LeaseExpiresAt)
}

// EndedUncleanly reports whether the attempt left its worktree without
// handing it back: its row never ended, or a reconcile ended it because its
// lease ran out. A process may still be running in that worktree, so the
// next claim quarantines it instead of reusing it.
func (a Attempt) EndedUncleanly() bool {
	return a.EndedAt == nil || a.EndReason == EndExpired
}

// Authorize decides whether a token-identified attempt may mutate task state
// right now. currentSeq is the task's current generation read in the same
// transaction. The check order gives the most useful error first.
func Authorize(a Attempt, currentSeq int, now time.Time) error {
	if a.Seq != currentSeq {
		return fault.New(fault.CodeInvalidSession, "session for %s was superseded by attempt %d; run `atask claim %s` to start a new attempt", a.TaskID, currentSeq, a.TaskID)
	}
	if a.EndReason == EndExpired {
		return fault.New(fault.CodeInvalidSession, "lease for %s expired at %s; run `atask claim %s` to resume with the saved handoff", a.TaskID, a.LeaseExpiresAt.UTC().Format(time.RFC3339), a.TaskID)
	}
	if a.EndedAt != nil {
		if a.EndReason == EndFinished {
			return fault.New(fault.CodeInvalidSession, "session for %s already finished; claim the task again to continue", a.TaskID)
		}
		return fault.New(fault.CodeInvalidSession, "session for %s has ended", a.TaskID)
	}
	if !now.Before(a.LeaseExpiresAt) {
		return fault.New(fault.CodeInvalidSession, "lease for %s expired at %s; run `atask claim %s` to resume with the saved handoff", a.TaskID, a.LeaseExpiresAt.UTC().Format(time.RFC3339), a.TaskID)
	}
	return nil
}
