package execution

import (
	"strings"
	"testing"
	"time"
)

// An attempt a reconcile ended because its lease ran out must read exactly
// like the open, lapsed attempt it was: interrupted, its worktree not
// reusable, and its token refused with the lease message.
func TestExpiredAttemptKeepsTheMeaningOfALapsedOne(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	lapsed := now.Add(-time.Hour)
	endedAt := now.Add(-time.Minute)
	open := Attempt{TaskID: "at-aaaaaa", Seq: 2, LeaseExpiresAt: lapsed}
	reaped := Attempt{TaskID: "at-aaaaaa", Seq: 2, LeaseExpiresAt: lapsed, EndedAt: &endedAt, EndReason: EndExpired}
	released := Attempt{TaskID: "at-aaaaaa", Seq: 2, LeaseExpiresAt: lapsed, EndedAt: &endedAt, EndReason: EndReleased}

	for name, a := range map[string]Attempt{"open": open, "reaped": reaped} {
		if !a.Interrupted(now) {
			t.Errorf("%s lapsed attempt is not interrupted", name)
		}
		if !a.EndedUncleanly() {
			t.Errorf("%s lapsed attempt does not report an unclean end", name)
		}
		err := Authorize(a, 2, now)
		if err == nil || !strings.Contains(err.Error(), "lease for at-aaaaaa expired") {
			t.Errorf("%s lapsed attempt: Authorize = %v, want the lease-expired message", name, err)
		}
	}
	if released.Interrupted(now) || released.EndedUncleanly() {
		t.Error("a released attempt handed its worktree back cleanly; it is not interrupted")
	}
}
