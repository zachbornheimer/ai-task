package app_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func mainRev(f *fixture) string { return strings.TrimSpace(f.git(f.repo, "rev-parse", "main")) }

// waitStatus polls until the task reports the wanted status.
func (f *fixture) waitStatus(ref string, want task.Status) {
	f.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if f.status(ref) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("%s never reached %s (now %s)", ref, want, f.status(ref))
}

// The promotion is bridged by a durable intent. A process killed at any
// point leaves a state that reconciliation resolves deterministically
// from Git: completion exactly when the candidate reached the target,
// otherwise a claimable task with no failure counted. While an intent is
// open nothing can move the task.
func TestIntegrationIntentCrashRecovery(t *testing.T) {
	cases := []struct {
		point    string
		promoted bool // the target moved before the crash
	}{
		{"final:after-checks", false},
		{"final:after-intent", false},
		{"final:after-promote", true},
		{"final:after-complete", true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.point, func(t *testing.T) {
			f := newGitFixture(t)
			a := f.add("a")
			s := f.claim(string(a))
			rev := f.commit(s.Workspace, "a.txt", "a")
			base := mainRev(f)
			crash := errors.New("simulated crash at " + c.point)
			app.SetFaultHook(f.e, func(p string) error {
				if p == c.point {
					return crash
				}
				return nil
			})
			_, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
			if !errors.Is(err, crash) {
				t.Fatalf("expected the injected crash, got %v", err)
			}
			app.SetFaultHook(f.e, nil)
			if moved := mainRev(f) == rev; moved != c.promoted {
				t.Fatalf("target moved=%v, want %v", moved, c.promoted)
			}
			v := f.show(string(a))
			switch c.point {
			case "final:after-checks":
				if v.Status != task.StatusVerifying || v.Integration != nil {
					t.Fatalf("after checks: %s %+v", v.Status, v.Integration)
				}
			case "final:after-intent", "final:after-promote":
				if v.Status != task.StatusAwaitingIntegration || v.Integration == nil || v.Integration.Candidate != rev || v.Integration.Base != base {
					t.Fatalf("open intent: %s %+v", v.Status, v.Integration)
				}
				// Nothing can move the task while the intent is open.
				_, err = f.e.Claim(f.ctx, app.ClaimRequest{TaskID: &a})
				wantCode(t, err, fault.CodeTaskAwaitingIntegration)
				_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Planner: true, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(a), Title: strptr("renamed")}}})
				wantCode(t, err, fault.CodePlanConflict)
				_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Planner: true, Operations: []plan.Change{plan.ArchiveTask{Target: plan.Ref(a), Reason: "x"}}})
				wantCode(t, err, fault.CodePlanConflict)
				_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Planner: true, Operations: []plan.Change{plan.AddTask{Key: "blk", Title: "blk", Blocks: []plan.Ref{plan.Ref(a)}, TaskChecks: okChecks()}}})
				wantCode(t, err, fault.CodePlanConflict)
			case "final:after-complete":
				if v.Status != task.StatusComplete {
					t.Fatalf("after complete: %s", v.Status)
				}
			}
			// The verifier is dead. Once its lease expires, any engine on the
			// store reconciles from Git.
			f.c.Advance(execution.DefaultLease + time.Minute)
			e2 := f.open()
			v2, err := e2.Show(f.ctx, f.proj.ID, string(a), true)
			if err != nil {
				t.Fatal(err)
			}
			if c.promoted {
				if v2.Status != task.StatusComplete || v2.CompletedRevision != rev || v2.Failures != 0 || len(v2.History.Integrations) != 1 {
					t.Fatalf("not completed by reconciliation: %s rev=%s failures=%d integrations=%d", v2.Status, v2.CompletedRevision, v2.Failures, len(v2.History.Integrations))
				}
				// The crashed caller's retry gets the stored acknowledgement.
				res, err := e2.Verify(f.ctx, s.Token, verification.ModeComplete)
				if err != nil || !res.Replayed || !res.Completed {
					t.Fatalf("replay: %+v %v", res, err)
				}
				return
			}
			if !v2.Status.Claimable() || v2.Failures != 0 || mainRev(f) != base || v2.Integration != nil {
				t.Fatalf("not released by reconciliation: %s failures=%d main=%s intent=%+v", v2.Status, v2.Failures, short(mainRev(f)), v2.Integration)
			}
			// Work resumes and completes normally.
			s2, err := e2.Claim(f.ctx, app.ClaimRequest{TaskID: &a})
			if err != nil || s2.AttemptSeq != 2 {
				t.Fatalf("re-claim: %+v %v", s2, err)
			}
			res, err := e2.Verify(f.ctx, s2.Token, verification.ModeComplete)
			if err != nil || !res.Completed || mainRev(f) != rev {
				t.Fatalf("second attempt: %+v %v", res, err)
			}
		})
	}
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// A contract change while checks run is refused in the intent transaction,
// before anything touches the target.
func TestPlannerEditDuringVerificationNeverMovesTarget(t *testing.T) {
	f := newGitFixture(t)
	flag := filepath.Join(t.TempDir(), "go")
	a := f.addWith("a", "while [ ! -f "+flag+" ]; do sleep 0.05; done")
	s := f.claim(string(a))
	f.commit(s.Workspace, "a.txt", "a")
	base := mainRev(f)
	done := make(chan error, 1)
	go func() { _, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete); done <- err }()
	f.waitStatus(string(a), task.StatusVerifying)
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Planner: true, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(a), Acceptance: plan.Replace([]string{"also this"})}}}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(flag, []byte("1"), 0o644)
	wantCode(t, <-done, fault.CodePlanConflict)
	if mainRev(f) != base {
		t.Fatal("target moved on a refused completion")
	}
	v := f.show(string(a))
	if v.Status != task.StatusClaimed || v.Integration != nil || v.Failures != 0 || v.LastError == "" {
		t.Fatalf("%s %+v failures=%d last_error=%q", v.Status, v.Integration, v.Failures, v.LastError)
	}
	// The same attempt verifies again under the new contract and completes.
	res, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	if err != nil || !res.Completed {
		t.Fatalf("%+v %v", res, err)
	}
}

// A verifier whose session was superseded mid-run can neither record an
// intent nor move the target; the successor completes normally.
func TestSupersededVerifierCannotPromote(t *testing.T) {
	f := newGitFixture(t)
	flag := filepath.Join(t.TempDir(), "go")
	a := f.addWith("a", "while [ ! -f "+flag+" ]; do sleep 0.05; done")
	sA := f.claim(string(a))
	f.commit(sA.Workspace, "a.txt", "a")
	base := mainRev(f)
	done := make(chan error, 1)
	go func() { _, err := f.e.Verify(f.ctx, sA.Token, verification.ModeComplete); done <- err }()
	f.waitStatus(string(a), task.StatusVerifying)
	f.c.Advance(execution.DefaultLease + time.Minute)
	sB := f.claim(string(a))
	os.WriteFile(flag, []byte("1"), 0o644)
	err := <-done
	if code := fault.CodeOf(err); code != fault.CodeSessionSuperseded && code != fault.CodeLeaseExpired {
		t.Fatalf("stale verifier result: %v", err)
	}
	if mainRev(f) != base {
		t.Fatal("stale verifier moved the target")
	}
	v := f.show(string(a))
	if v.Status != task.StatusClaimed || v.Attempt == nil || v.Attempt.Seq != sB.AttemptSeq || v.Integration != nil || v.Failures != 0 {
		t.Fatalf("%+v", v)
	}
	f.complete(sB)
}

// Cohort promotions use the same intent protocol, one row per member.
func TestCohortIntentCrashRecovery(t *testing.T) {
	for _, point := range []string{"cohort:after-intent", "cohort:after-promote"} {
		point := point
		t.Run(point, func(t *testing.T) {
			f := newGitFixture(t)
			res := f.apply(
				plan.AddTask{Key: "x", Title: "x", Cohort: "c", TaskChecks: []verification.CheckSpec{check("ux", "test -f x.txt", true)}},
				plan.AddTask{Key: "y", Title: "y", Cohort: "c", TaskChecks: []verification.CheckSpec{check("uy", "test -f y.txt", true)}},
			)
			x, y := res.Created["x"], res.Created["y"]
			sx := f.claim(string(x))
			f.commit(sx.Workspace, "x.txt", "x")
			sy := f.claim(string(y))
			f.commit(sy.Workspace, "y.txt", "y")
			if _, err := f.e.Verify(f.ctx, sx.Token, verification.ModeComplete); err != nil {
				t.Fatal(err)
			}
			base := mainRev(f)
			crash := errors.New("simulated crash at " + point)
			app.SetFaultHook(f.e, func(p string) error {
				if p == point {
					return crash
				}
				return nil
			})
			_, err := f.e.Verify(f.ctx, sy.Token, verification.ModeComplete)
			if !errors.Is(err, crash) {
				t.Fatalf("expected the injected crash, got %v", err)
			}
			app.SetFaultHook(f.e, nil)
			promoted := point == "cohort:after-promote"
			if (mainRev(f) != base) != promoted {
				t.Fatalf("target moved=%v want %v", mainRev(f) != base, promoted)
			}
			for _, id := range []task.ID{x, y} {
				if v := f.show(string(id)); v.Status != task.StatusAwaitingIntegration || v.Integration == nil {
					t.Fatalf("%s: %s %+v", id, v.Status, v.Integration)
				}
			}
			// The verifier is gone; the job lease expires; reconciliation decides.
			f.c.Advance(execution.DefaultLease + time.Minute)
			e2 := f.open()
			if promoted {
				for _, id := range []task.ID{x, y} {
					v, _ := e2.Show(f.ctx, f.proj.ID, string(id), true)
					if v.Status != task.StatusComplete || v.Failures != 0 || len(v.History.Integrations) != 1 {
						t.Fatalf("%s not completed by reconciliation: %s", id, v.Status)
					}
				}
				return
			}
			for _, id := range []task.ID{x, y} {
				v, _ := e2.Show(f.ctx, f.proj.ID, string(id), false)
				if v.Status != task.StatusAwaitingVerification || v.Failures != 0 {
					t.Fatalf("%s after abandon: %s failures=%d", id, v.Status, v.Failures)
				}
			}
			if mainRev(f) != base {
				t.Fatal("target moved")
			}
			// Any worker rebuilds the candidate and completes the cohort.
			if ran, err := e2.RunPendingCohorts(f.ctx, f.proj.ID); err != nil || !ran {
				t.Fatalf("retry: ran=%v %v", ran, err)
			}
			for _, id := range []task.ID{x, y} {
				if v, _ := e2.Show(f.ctx, f.proj.ID, string(id), false); v.Status != task.StatusComplete {
					t.Fatalf("%s: %s", id, v.Status)
				}
			}
			if mainRev(f) == base {
				t.Fatal("cohort not promoted")
			}
		})
	}
}
