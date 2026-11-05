package app_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/must"
)

// attemptEnd reads how the only attempt of a task ended: whether it did,
// and the recorded reason.
func (f *fixture) attemptEnd(id string) (ended bool, reason string) {
	f.t.Helper()
	db, err := sql.Open("sqlite", f.path)
	must.OK(f.t, err)
	defer discard.CloseReadOnly(db)
	var endedAt sql.NullInt64
	var why sql.NullString
	must.OK(f.t, db.QueryRow(`SELECT ended_at, end_reason FROM execution_attempts WHERE task_id = ? ORDER BY seq DESC LIMIT 1`, id).Scan(&endedAt, &why))
	return endedAt.Valid, why.String
}

// TestReconcileEndsExpiredAttempts: an attempt whose lease ran out is dead
// whether or not anything claims the task again, so the next reconcile
// ends it. Left open, such rows pile up (190 on the production board) and
// every query that asks "is this attempt still open?" has to rule them out.
func TestReconcileEndsExpiredAttempts(t *testing.T) {
	clearRemoteStore(t)
	f := newFixture(t)
	id := f.add("lapsed")
	f.claim(string(id))
	if ended, _ := f.attemptEnd(string(id)); ended {
		t.Fatal("a live claim's attempt is already ended")
	}
	f.c.Advance(2 * time.Hour) // far past the lease
	f.show(string(id))         // any read reconciles first
	ended, reason := f.attemptEnd(string(id))
	if !ended {
		t.Fatal("the expired attempt is still open after a reconcile")
	}
	if reason != string(execution.EndExpired) {
		t.Fatalf("end reason = %q, want %q", reason, execution.EndExpired)
	}
	f.claim(string(id)) // and the task is takeable again
}

// TestReconcileLeavesLiveAttemptsOpen: a claim inside its lease is never
// ended by a reconcile.
func TestReconcileLeavesLiveAttemptsOpen(t *testing.T) {
	clearRemoteStore(t)
	f := newFixture(t)
	id := f.add("live")
	f.claim(string(id))
	f.c.Advance(time.Minute)
	f.show(string(id))
	if ended, _ := f.attemptEnd(string(id)); ended {
		t.Fatal("a reconcile ended a live claim's attempt")
	}
}
