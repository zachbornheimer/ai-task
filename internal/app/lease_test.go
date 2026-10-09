package app_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// A renewal extends by the attempt's own lease and never shortens it.
func TestRenewNeverShortensLease(t *testing.T) {
	f := newGitFixture(t)
	a := f.add("a")
	s, err := f.e.Claim(f.ctx, app.ClaimRequest{TaskID: &a, Lease: 4 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	start := f.c.Now()
	if !s.LeaseUntil.Equal(start.Add(4 * time.Hour)) {
		t.Fatalf("lease until %s", s.LeaseUntil)
	}
	f.c.Advance(5 * time.Minute)
	exp, err := f.e.Renew(f.ctx, s.Token)
	if err != nil || !exp.Equal(f.c.Now().Add(4*time.Hour)) {
		t.Fatalf("renew: %s %v (want %s)", exp, err, f.c.Now().Add(4*time.Hour))
	}
	// Implicit renewal by an authenticated command behaves the same.
	f.c.Advance(5 * time.Minute)
	if _, err := f.e.Log(f.ctx, s.Token, execution.LogEntry{Note: "alive"}); err != nil {
		t.Fatal(err)
	}
	if v := f.show(string(a)); !v.Attempt.LeaseExpiresAt.Equal(f.c.Now().Add(4 * time.Hour)) {
		t.Fatalf("after log: %s", v.Attempt.LeaseExpiresAt)
	}
	// A default-lease attempt renewed by a 30m command keeps 30m, not less.
	b := f.add("b")
	sb := f.claim(string(b))
	f.c.Advance(time.Minute)
	exp, _ = f.e.Renew(f.ctx, sb.Token)
	if !exp.Equal(f.c.Now().Add(execution.DefaultLease)) {
		t.Fatalf("default renew: %s", exp)
	}
}

// The heartbeat survives transient renewal errors and aborts the external
// work at once on a terminal authority error.
func TestHeartbeatRecoversFromTransientAndAbortsOnTerminal(t *testing.T) {
	f := newGitFixture(t)
	e, err := app.Open(f.ctx, app.Config{Path: f.path, Now: f.c.Now, PollInterval: 50 * time.Millisecond, HeartbeatInterval: 40 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	// Transient: the first renewals fail with a busy-style INTERNAL error;
	// the run continues and completes once the check finishes.
	flag := filepath.Join(t.TempDir(), "go")
	a := f.addWith("a", "while [ ! -f "+flag+" ]; do sleep 0.02; done")
	s, err := e.Claim(f.ctx, app.ClaimRequest{TaskID: &a})
	if err != nil {
		t.Fatal(err)
	}
	f.commit(s.Workspace, "a.txt", "a")
	var calls atomic.Int32
	app.SetFaultHook(e, func(p string) error {
		if p == "heartbeat:renew" && calls.Add(1) <= 2 {
			return fault.New(fault.CodeInternal, "database is locked")
		}
		return nil
	})
	done := make(chan error, 1)
	go func() { _, err := e.Verify(f.ctx, s.Token, verification.ModeComplete); done <- err }()
	deadline := time.Now().Add(10 * time.Second)
	for calls.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	os.WriteFile(flag, []byte("1"), 0o644)
	if err := <-done; err != nil {
		t.Fatalf("transient renewal errors must not abort the run: %v", err)
	}
	if f.status(string(a)) != task.StatusComplete {
		t.Fatal("not complete")
	}

	// Terminal: a superseded session aborts the running check immediately,
	// before its flag ever appears, with the reason, and nothing is counted.
	flag2 := filepath.Join(t.TempDir(), "go2")
	b := f.addWith("b", "while [ ! -f "+flag2+" ]; do sleep 0.02; done")
	sb, err := e.Claim(f.ctx, app.ClaimRequest{TaskID: &b})
	if err != nil {
		t.Fatal(err)
	}
	f.commit(sb.Workspace, "b.txt", "b")
	app.SetFaultHook(e, func(p string) error {
		if p == "heartbeat:renew" {
			return fault.New(fault.CodeSessionSuperseded, "simulated supersession")
		}
		return nil
	})
	started := time.Now()
	_, err = e.Verify(f.ctx, sb.Token, verification.ModeComplete)
	if !errors.Is(err, err) || !terminalCode(fault.CodeOf(err)) {
		t.Fatalf("expected a terminal authority error, got %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("abort was not prompt")
	}
	app.SetFaultHook(e, nil)
	if v := f.show(string(b)); v.Failures != 0 || v.Status == task.StatusComplete {
		t.Fatalf("aborted run misrecorded: %+v", v)
	}
}

func terminalCode(c fault.Code) bool {
	return c == fault.CodeSessionSuperseded || c == fault.CodeLeaseExpired || c == fault.CodeSessionFinished
}
