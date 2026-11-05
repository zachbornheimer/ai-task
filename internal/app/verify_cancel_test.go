package app_test

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// newestRun reads the newest verification run's status and summary.
func (f *fixture) newestRun() (status, summary string) {
	f.t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	defer discard.CloseReadOnly(db)
	if err := db.QueryRow(`SELECT status, summary FROM verification_runs ORDER BY id DESC LIMIT 1`).Scan(&status, &summary); err != nil {
		f.t.Fatal(err)
	}
	return status, summary
}

// TestVerifyCancelClosesLiveRun: a `verify complete` that is wedged (its
// caller gave up, its process still stamps heartbeats) can be closed by the
// session that owns it, without waiting for the reaper's two missed
// heartbeats, and without costing the task an attempt.
func TestVerifyCancelClosesLiveRun(t *testing.T) {
	clearRemoteStore(t)
	f := newGitFixture(t)
	e, s, id, cancel := startSleepingVerify(t, f)
	defer cancel()
	res, err := e.CancelVerify(f.ctx, s.Token)
	if err != nil {
		t.Fatal(err)
	}
	if res.Closed != 1 {
		t.Fatalf("closed %d runs, want 1", res.Closed)
	}
	status, summary := f.newestRun()
	if status != "error" || !strings.HasPrefix(summary, "abandoned:") || !strings.Contains(summary, "canceled") {
		t.Fatalf("run = %s %q, want an abandoned error naming the cancel", status, summary)
	}
	if got := f.status(string(id)); got != task.StatusClaimed {
		t.Fatalf("status %s after cancel, want claimed (the claim is still live)", got)
	}
	again, err := e.CancelVerify(f.ctx, s.Token)
	if err != nil {
		t.Fatal(err)
	}
	if again.Closed != 0 {
		t.Fatalf("a second cancel closed %d runs, want 0", again.Closed)
	}
}

// TestAbandonVerifyClosesTheRunAsTerminated: the CLI calls this after a
// SIGTERM or SIGHUP stopped its verification, so the run reads as lost to
// a termination, not as a user's cancel, and still costs no attempt.
func TestAbandonVerifyClosesTheRunAsTerminated(t *testing.T) {
	clearRemoteStore(t)
	f := newGitFixture(t)
	e, s, id, cancel := startSleepingVerify(t, f)
	defer cancel()
	res, err := e.AbandonVerify(f.ctx, s.Token)
	if err != nil {
		t.Fatal(err)
	}
	if res.Closed != 1 {
		t.Fatalf("closed %d runs, want 1", res.Closed)
	}
	status, summary := f.newestRun()
	if status != "error" || !strings.HasPrefix(summary, "abandoned:") || !strings.Contains(summary, "terminated") {
		t.Fatalf("run = %s %q, want an abandoned error naming the termination", status, summary)
	}
	if got := f.status(string(id)); got != task.StatusClaimed {
		t.Fatalf("status %s, want claimed", got)
	}
}

// TestCanceledRunCannotComplete: the process whose run was canceled may
// still be alive; its checks finishing must not complete the task.
func TestCanceledRunCannotComplete(t *testing.T) {
	clearRemoteStore(t)
	f := newGitFixture(t)
	e := f.realClockEngineWithLease(orphanTestLease, orphanPulseInterval)
	id := f.addWith("cancel-race", "sleep 2")
	s, err := e.Claim(f.ctx, app.ClaimRequest{TaskID: &id})
	if err != nil {
		t.Fatal(err)
	}
	f.commit(s.Workspace, "c.txt", "c")
	done := make(chan error, 1)
	go func() {
		_, verr := e.Verify(f.ctx, s.Token, verification.ModeComplete)
		done <- verr
	}()
	deadline := time.Now().Add(verifyingWait)
	for f.status(string(id)) != task.StatusVerifying {
		if time.Now().After(deadline) {
			t.Fatal("task never reached verifying")
		}
		time.Sleep(verifyingPoll)
	}
	if _, err := e.CancelVerify(f.ctx, s.Token); err != nil {
		t.Fatal(err)
	}
	if verr := <-done; verr == nil {
		t.Fatal("the canceled run's verify returned success")
	}
	if got := f.status(string(id)); got == task.StatusComplete {
		t.Fatal("a canceled run completed the task")
	}
}
