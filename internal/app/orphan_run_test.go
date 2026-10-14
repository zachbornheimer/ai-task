package app_test

import (
	"context"
	"database/sql"
	"runtime"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

const staleHeartbeatSQL = `UPDATE verification_runs SET heartbeat_every_ms = 1000, heartbeat_at = ? WHERE status = 'running'`

const (
	verifyingWait = 2 * time.Minute
	verifyingPoll = 20 * time.Millisecond
)

func startSleepingVerify(t *testing.T, f *fixture) (*app.Engine, app.Session, task.ID, context.CancelFunc) {
	t.Helper()
	e := f.realClockEngine()
	id := f.addWith("orphan", "sleep 30")
	s, err := e.Claim(f.ctx, app.ClaimRequest{TaskID: &id})
	if err != nil {
		t.Fatal(err)
	}
	f.commit(s.Workspace, "o.txt", "o")
	vctx, cancel := context.WithCancel(f.ctx)
	verifyDone := make(chan struct{})
	var verifyErr error
	go func() {
		defer close(verifyDone)
		_, verifyErr = e.Verify(vctx, s.Token, verification.ModeComplete)
	}()
	// Registered after f's TempDir cleanup, so it runs before it: the run
	// must be canceled and joined before its files are removed.
	t.Cleanup(func() { cancel(); <-verifyDone })
	// Verify blocks for the whole run, so the only signal that the run has
	// started is the task state. Poll it, but give a loaded machine ample
	// time and fail with Verify's own error if it ended before starting.
	deadline := time.Now().Add(verifyingWait)
	for time.Now().Before(deadline) {
		if v, err := e.Show(f.ctx, f.proj.ID, string(id), false); err == nil && v.Status == task.StatusVerifying {
			return e, s, id, cancel
		}
		select {
		case <-verifyDone:
			t.Fatalf("verify ended before the task reached verifying: %v", verifyErr)
		case <-time.After(verifyingPoll):
		}
	}
	t.Fatalf("task never reached verifying within %s", verifyingWait)
	return nil, s, id, cancel
}

func TestOrphanedVerificationRunIsReaped(t *testing.T) {
	f := newGitFixture(t)
	e, s, id, cancel := startSleepingVerify(t, f)
	f.sql(staleHeartbeatSQL, time.Now().Add(-time.Hour).UnixMilli())
	cancel() // the process is gone

	v, err := e.Show(f.ctx, f.proj.ID, string(id), false)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status == task.StatusVerifying {
		t.Fatalf("status still verifying after a stale heartbeat")
	}
	if v.Status != task.StatusClaimed {
		t.Fatalf("status %s, want claimed (lease is live)", v.Status)
	}
	var summary, status string
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer discard.CloseReadOnly(db)
	if err := db.QueryRow(`SELECT status, summary FROM verification_runs ORDER BY id DESC LIMIT 1`).Scan(&status, &summary); err != nil {
		t.Fatal(err)
	}
	if status != "error" || summary != "process lost" {
		t.Fatalf("run = %s %q, want error \"process lost\"", status, summary)
	}
	if err := e.Release(f.ctx, app.ReleaseRequest{Token: s.Token}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Claim(f.ctx, app.ClaimRequest{TaskID: &id}); err != nil {
		t.Fatalf("claim after reap: %v", err)
	}
}

func TestResetAttemptsAfterLostRunReportsNoChange(t *testing.T) {
	f := newGitFixture(t)
	e, _, id, cancel := startSleepingVerify(t, f)
	f.sql(staleHeartbeatSQL, time.Now().Add(-time.Hour).UnixMilli())
	cancel()
	r, err := e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(id), ResetAttempts: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Changed != 0 {
		t.Fatalf("reset reported changed=%d with nothing to reset", r.Changed)
	}
	if st := f.status(string(id)); st == task.StatusVerifying {
		t.Fatal("still verifying")
	}
}

func TestLiveVerificationRunIsNotReaped(t *testing.T) {
	f := newGitFixture(t)
	e, _, id, _ := startSleepingVerify(t, f)
	f.sql(staleHeartbeatSQL, time.Now().UnixMilli())
	v, err := e.Show(f.ctx, f.proj.ID, string(id), false)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != task.StatusVerifying {
		t.Fatalf("live run reaped: status %s", v.Status)
	}
}

const (
	// pulseTestLease is far longer than the 0.2s run, so scheduler delay under
	// load cannot expire the claim; the renewal interval is set separately
	// so the pulse still ticks several times during the run.
	pulseTestLease = time.Minute
	pulseInterval  = 50 * time.Millisecond
	// pulseQuietPeriod is three pulse intervals: a leaked pulse would tick
	// within it.
	pulseQuietPeriod = 3 * pulseInterval
)

// The run pulse is joined before Verify returns: once the run ends, no
// goroutine survives and the run's heartbeat is never written again.
func TestRunPulseStopsWithRun(t *testing.T) {
	f := newGitFixture(t)
	e := f.realClockEngineWithLease(pulseTestLease, pulseInterval)
	id := f.addWith("pulse", "sleep 0.2")
	s, err := e.Claim(f.ctx, app.ClaimRequest{TaskID: &id})
	if err != nil {
		t.Fatal(err)
	}
	f.commit(s.Workspace, "p.txt", "p")
	before := runtime.NumGoroutine()
	if _, err := e.Verify(f.ctx, s.Token, verification.ModeComplete); err != nil {
		t.Fatal(err)
	}
	beat := func() int64 {
		db, err := sql.Open("sqlite", f.path)
		if err != nil {
			t.Fatal(err)
		}
		defer discard.CloseReadOnly(db)
		var at int64
		if err := db.QueryRow(`SELECT heartbeat_at FROM verification_runs ORDER BY id DESC LIMIT 1`).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	first := beat()
	time.Sleep(pulseQuietPeriod)
	if second := beat(); second != first {
		t.Fatalf("heartbeat written after Verify returned: %d then %d", first, second)
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutines leaked by the run: %d before, %d after", before, after)
	}
}
