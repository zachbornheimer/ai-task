package app_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/must"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

const (
	testLease      = 2 * time.Second
	testRunLength  = "5"
	supersedeAfter = 700 * time.Millisecond
)

// realClockEngine opens another engine on the fixture database that uses
// wall-clock time and accepts sub-minute leases, so a run can genuinely
// outlive its lease.
func (f *fixture) realClockEngine() *app.Engine {
	e, err := app.Open(f.ctx, app.Config{
		Path: f.path, PollInterval: 50 * time.Millisecond, Lease: testLease,
		Switch: addSwitcher{t: f.t, repo: f.repo, root: filepath.Join(filepath.Dir(f.repo), "ws")},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { must.OK(f.t, e.Close()) })
	return e
}

// A verification run renews its attempt lease while it executes, so a run
// longer than the lease still completes.
func TestVerifyRenewsLeaseDuringRun(t *testing.T) {
	f := newGitFixture(t)
	e := f.realClockEngine()
	a := f.addWith("a", "sleep "+testRunLength)
	s, err := e.Claim(f.ctx, app.ClaimRequest{TaskID: &a})
	if err != nil {
		t.Fatal(err)
	}
	f.commit(s.Workspace, "a.txt", "a")
	original := s.LeaseUntil

	res, err := e.Verify(f.ctx, s.Token, verification.ModeComplete)
	if err != nil {
		t.Fatalf("a run longer than the lease must complete: %v (%s)", err, res.Summary)
	}
	if st := f.status(string(a)); st != task.StatusComplete {
		t.Fatalf("status %s, want complete", st)
	}
	v, err := e.Show(f.ctx, f.proj.ID, string(a), false)
	if err != nil {
		t.Fatal(err)
	}
	if v.Attempt == nil || !v.Attempt.LeaseExpiresAt.After(original) {
		t.Fatalf("lease was not renewed during the run: original %s, after %+v", original, v.Attempt)
	}
}

// Losing the session mid-run cancels the run and the task stays incomplete.
func TestVerifyCancelsRunWhenSessionLostMidRun(t *testing.T) {
	f := newGitFixture(t)
	e := f.realClockEngine()
	b := f.addWith("b", "sleep "+testRunLength)
	s, err := e.Claim(f.ctx, app.ClaimRequest{TaskID: &b})
	if err != nil {
		t.Fatal(err)
	}
	f.commit(s.Workspace, "b.txt", "b")
	go func() {
		time.Sleep(supersedeAfter)
		_ = e.Release(f.ctx, app.ReleaseRequest{Token: s.Token})
	}()
	started := time.Now()
	_, err = e.Verify(f.ctx, s.Token, verification.ModeComplete)
	if err == nil || !terminalCode(fault.CodeOf(err)) {
		t.Fatalf("expected a terminal authority error, got %v", err)
	}
	if time.Since(started) > 4*time.Second {
		t.Fatal("run was not canceled promptly")
	}
	if f.status(string(b)) == task.StatusComplete {
		t.Fatal("task completed despite lost session")
	}
}
