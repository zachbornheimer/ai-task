package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

func TestClaimIsDeterministicOldestFirst(t *testing.T) {
	f := newFixture(t)
	var ids []task.ID
	for _, k := range []string{"one", "two", "three"} {
		ids = append(ids, f.add(k))
		f.c.Advance(time.Second)
	}
	for _, want := range ids {
		s := f.claim("")
		if s.Task.ID != want {
			t.Fatalf("got %s want %s", s.Task.ID, want)
		}
	}
	_, err := f.e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID})
	wantCode(t, err, fault.CodeNoAvailableTask)
}

func TestConcurrentClaimsYieldDistinctSessions(t *testing.T) {
	f := newFixture(t)
	const ready, callers = 5, 20
	for i := 0; i < ready; i++ {
		f.add(string(rune('a' + i)))
	}
	var mu sync.Mutex
	got := map[task.ID]int{}
	var none int32
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := f.e
			if i%2 == 1 {
				e = f.open()
			}
			s, err := e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID})
			if err != nil {
				if fault.Is(err, fault.CodeNoAvailableTask) {
					atomic.AddInt32(&none, 1)
					return
				}
				t.Error(err)
				return
			}
			mu.Lock()
			got[s.Task.ID]++
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if len(got) != ready || int(none) != callers-ready {
		t.Fatalf("distinct=%d none=%d", len(got), none)
	}
	for id, n := range got {
		if n != 1 {
			t.Fatalf("%s claimed %d times", id, n)
		}
	}
}

func TestExplicitClaimEnforcesEligibility(t *testing.T) {
	f := newFixture(t)
	a := f.add("a")
	b := f.add("b", "a")
	_, err := f.e.Claim(f.ctx, app.ClaimRequest{TaskID: &b})
	wantCode(t, err, fault.CodeTaskBlocked)
	f.claim(string(a))
	_, err = f.e.Claim(f.ctx, app.ClaimRequest{TaskID: &a})
	wantCode(t, err, fault.CodeTaskAlreadyTaken)
}

func TestTokenFencingAcrossExpiryAndSupersession(t *testing.T) {
	f := newFixture(t)
	a := f.add("a")
	s1, err := f.e.Claim(f.ctx, app.ClaimRequest{TaskID: &a, Lease: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	f.e.Log(f.ctx, s1.Token, execution.LogEntry{Done: "half", Next: "other half", Learned: "x is y"})
	f.c.Advance(31 * time.Minute)
	if f.status(string(a)) != task.StatusInterrupted {
		t.Fatalf("%s", f.status(string(a)))
	}
	for _, op := range []func() error{
		func() error { _, err := f.e.Log(f.ctx, s1.Token, execution.LogEntry{Done: "late"}); return err },
		func() error { _, err := f.e.Renew(f.ctx, s1.Token); return err },
		func() error { _, err := f.e.Release(f.ctx, s1.Token, app.ReleaseOptions{}); return err },
		func() error { _, err := f.e.Verify(f.ctx, s1.Token, verification.ModeComplete); return err },
	} {
		wantCode(t, op(), fault.CodeLeaseExpired)
	}
	// Another worker resumes with the durable handoff.
	s2, err := f.open().Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID})
	if err != nil || s2.Task.ID != a || !s2.Resumed || s2.AttemptSeq != 2 || s2.Handoff.LatestNext != "other half" || len(s2.Handoff.Learnings) != 1 {
		t.Fatalf("%+v %v", s2, err)
	}
	for _, op := range []func() error{
		func() error { _, err := f.e.Log(f.ctx, s1.Token, execution.LogEntry{Done: "zombie"}); return err },
		func() error { _, err := f.e.Renew(f.ctx, s1.Token); return err },
		func() error { _, err := f.e.Verify(f.ctx, s1.Token, verification.ModeTask); return err },
	} {
		wantCode(t, op(), fault.CodeSessionSuperseded)
	}
	if _, err := f.e.Log(f.ctx, s2.Token, execution.LogEntry{Done: "resumed"}); err != nil {
		t.Fatal(err)
	}
	// Renewal is atomic and bounded; unknown tokens are invalid.
	exp, err := f.e.Renew(f.ctx, s2.Token)
	if err != nil || !exp.Equal(f.c.Now().Add(execution.DefaultLease)) {
		t.Fatalf("%v %v", exp, err)
	}
	_, err = f.e.Renew(f.ctx, execution.NewToken())
	wantCode(t, err, fault.CodeInvalidSession)
}

func TestContinuousQueueReleasesDependentsImmediately(t *testing.T) {
	f := newGitFixture(t)
	a := f.add("a")
	f.add("b")
	f.add("c")
	d := f.add("d", "a")
	sa, sb, sc := f.claim(string(a)), f.claim("b"), f.claim("c")
	_ = sb
	_ = sc
	if f.status(string(d)) != task.StatusBlocked {
		t.Fatal("d should be blocked")
	}
	f.complete(sa)
	// b and c are still claimed, yet d is claimable right now.
	sd, err := f.e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID})
	if err != nil || sd.Task.ID != d {
		t.Fatalf("%+v %v", sd, err)
	}
}

func TestWaitingClaimDoneAndStalled(t *testing.T) {
	f := newGitFixture(t)
	a := f.add("a")
	b := f.add("b", "a")
	// A waiting claim takes a, and a second waiter blocks until a is
	// complete, then gets b.
	sa := f.claim(string(a))
	done := make(chan app.Session, 1)
	errs := make(chan error, 1)
	go func() {
		s, err := f.open().Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID, Wait: true})
		if err != nil {
			errs <- err
			return
		}
		done <- s
	}()
	select {
	case s := <-done:
		t.Fatalf("waiter returned early with %s", s.Task.ID)
	case err := <-errs:
		t.Fatalf("waiter failed: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	f.complete(sa)
	select {
	case s := <-done:
		if s.Task.ID != b {
			t.Fatalf("waiter got %s", s.Task.ID)
		}
		f.complete(s)
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("waiter not woken")
	}
	// Everything complete: ErrDone, matched by errors.Is.
	_, err := f.e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID, Wait: true})
	if !errors.Is(err, app.ErrDone) {
		t.Fatalf("want ErrDone, got %v", err)
	}
	// Exhausted retries: stalled with reasons; a reset makes it claimable.
	x := f.addWith("x", "exit 1")
	p, _ := f.e.Project(f.ctx, f.proj.ID)
	p.MaxAttempts = 1
	f.e.UpdateProject(f.ctx, p)
	sx := f.claim(string(x))
	f.commit(sx.Workspace, "x.txt", "x")
	_, err = f.e.Verify(f.ctx, sx.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeVerificationFailed)
	f.e.Release(f.ctx, sx.Token, app.ReleaseOptions{})
	if f.status(string(x)) != task.StatusNeedsAttention {
		v := f.show(string(x))
		pp, _ := f.e.Project(f.ctx, f.proj.ID)
		t.Fatalf("%s failures=%d max=%d err=%v", v.Status, v.Failures, pp.MaxAttempts, err)
	}
	_, err = f.e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID, Wait: true})
	if !errors.Is(err, app.ErrStalled) {
		t.Fatalf("want ErrStalled, got %v", err)
	}
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Details == nil {
		t.Fatal("stall details missing")
	}
	f.apply(plan.UpdateTask{Target: plan.Ref(x), ResetAttempts: true, TaskChecks: plan.Replace([]verification.CheckSpec{check("unit-x", "true", true)})})
	// The last judgement stays visible, but the task is claimable again.
	if st := f.status(string(x)); !st.Claimable() || st != task.StatusVerificationFailed {
		t.Fatalf("%s", st)
	}
	sx2 := f.claim(string(x))
	f.complete(sx2)
	// Cancellation returns ctx.Err without spinning.
	y := f.add("y")
	f.claim(string(y))
	ctx, cancel := context.WithTimeout(f.ctx, 500*time.Millisecond)
	defer cancel()
	_, err = f.e.Claim(ctx, app.ClaimRequest{ProjectID: f.proj.ID, Wait: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want ctx error, got %v", err)
	}
}

func TestReleaseAndCooldown(t *testing.T) {
	f := newFixture(t)
	p, _ := f.e.Project(f.ctx, f.proj.ID)
	p.RetryCooldown = 10 * time.Minute
	f.e.UpdateProject(f.ctx, p)
	a := f.add("a")
	s := f.claim(string(a))
	v, err := f.e.Release(f.ctx, s.Token, app.ReleaseOptions{Note: "giving up for now", Failed: true})
	if err != nil || v.Status != task.StatusCooldown || v.Failures != 1 {
		t.Fatalf("%+v %v", v, err)
	}
	_, err = f.e.Claim(f.ctx, app.ClaimRequest{TaskID: &a})
	wantCode(t, err, fault.CodeNoAvailableTask)
	f.c.Advance(11 * time.Minute)
	s2 := f.claim(string(a))
	if s2.Handoff.RecentLogs[0].Note != "giving up for now" {
		t.Fatalf("%+v", s2.Handoff)
	}
	// Released without --failed: immediately claimable, no failure.
	v, _ = f.e.Release(f.ctx, s2.Token, app.ReleaseOptions{})
	if v.Status != task.StatusReady || v.Failures != 1 {
		t.Fatalf("%+v", v)
	}
	_, err = f.e.Release(f.ctx, s2.Token, app.ReleaseOptions{})
	wantCode(t, err, fault.CodeSessionSuperseded)
}

func TestHandoffCarriesPrerequisiteLearningsAndLastFailure(t *testing.T) {
	f := newFixture(t)
	a := f.add("a")
	b := f.add("b", plan.Ref(a))
	sa := f.claim(string(a))
	if _, err := f.e.Log(f.ctx, sa.Token, execution.LogEntry{Learned: "the API returns 204 on success"}); err != nil {
		t.Fatal(err)
	}
	f.complete(sa)
	sb := f.claim(string(b))
	if len(sb.Handoff.Inherited) != 1 || sb.Handoff.Inherited[0].TaskID != string(a) || sb.Handoff.Inherited[0].Key != "a" || sb.Handoff.Inherited[0].Learned != "the API returns 204 on success" {
		t.Fatalf("inherited: %+v", sb.Handoff.Inherited)
	}
	if sb.Handoff.LastFailure != nil {
		t.Fatalf("no failure yet: %+v", sb.Handoff.LastFailure)
	}
	// A failed verification shows up in the next attempt's handoff with the
	// failing checks and their output.
	c := f.addWith("c", "echo boom >&2; exit 3")
	sc := f.claim(string(c))
	f.commit(sc.Workspace, "c.txt", "c")
	_, err := f.e.Verify(f.ctx, sc.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeVerificationFailed)
	if _, err := f.e.Release(f.ctx, sc.Token, app.ReleaseOptions{Note: "stuck"}); err != nil {
		t.Fatal(err)
	}
	sc2 := f.claim(string(c))
	lf := sc2.Handoff.LastFailure
	if lf == nil || lf.Mode != string(verification.ModeComplete) || len(lf.Checks) != 1 || lf.Checks[0].CheckID != "unit-c" || !strings.Contains(lf.Checks[0].StderrTail, "boom") {
		t.Fatalf("last failure: %+v", lf)
	}
	// Worktree reuse: the previous attempt's commit is still there and the
	// token stored in the worktree is the new attempt's.
	if _, err := os.Stat(filepath.Join(sc2.Workspace, "c.txt")); err != nil {
		t.Fatal("work not carried over")
	}
	if tok := workspace.LoadToken(f.ctx, sc2.Workspace); tok != string(sc2.Token) {
		t.Fatalf("stored token %q != %q", tok, sc2.Token)
	}
	if _, err := os.Stat(filepath.Join(sc2.Workspace, "at-session")); err == nil {
		t.Fatal("token stored inside the tree")
	}
}
