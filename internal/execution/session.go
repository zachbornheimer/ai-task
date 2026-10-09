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

// Lease bounds. The default is long enough for an interactive agent to work
// without renewing on every step, short enough that a crashed agent frees the
// task within a working session.
const (
	DefaultLease = time.Hour
	MinLease     = time.Second
	MaxLease     = 24 * time.Hour
)

// LeaseDuration resolves a requested lease length against the bounds.
// Zero means DefaultLease.
func LeaseDuration(requested time.Duration) (time.Duration, error) {
	if requested == 0 {
		return DefaultLease, nil
	}
	if requested < MinLease || requested > MaxLease {
		return 0, fault.New(fault.CodeInvalidInput, "lease must be between %s and %s", MinLease, MaxLease)
	}
	return requested, nil
}

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
	EndedAt        *time.Time
	EndReason      EndReason
}

// LeaseActive reports whether the attempt currently holds authority.
func (a Attempt) LeaseActive(now time.Time) bool {
	return a.EndedAt == nil && now.Before(a.LeaseExpiresAt)
}

// Interrupted reports whether the attempt stopped without finishing: its
// lease ran out while it was still current.
func (a Attempt) Interrupted(now time.Time) bool {
	return a.EndedAt == nil && !now.Before(a.LeaseExpiresAt)
}

// Authorize decides whether a token-identified attempt may mutate task state
// right now. currentSeq is the task's current generation read in the same
// transaction. The check order gives the most useful error first.
func Authorize(a Attempt, currentSeq int, now time.Time) error {
	if a.Seq != currentSeq {
		return fault.New(fault.CodeSessionSuperseded, "session for %s was superseded by attempt %d; run `tasks take %s` to start a new attempt", a.TaskID, currentSeq, a.TaskID)
	}
	if a.EndedAt != nil {
		if a.EndReason == EndFinished {
			return fault.New(fault.CodeSessionFinished, "session for %s already finished; take the task again to continue", a.TaskID)
		}
		return fault.New(fault.CodeSessionSuperseded, "session for %s has ended", a.TaskID)
	}
	if !now.Before(a.LeaseExpiresAt) {
		return fault.New(fault.CodeLeaseExpired, "lease for %s expired at %s; run `tasks take %s` to resume with the saved handoff", a.TaskID, a.LeaseExpiresAt.UTC().Format(time.RFC3339), a.TaskID)
	}
	return nil
}
