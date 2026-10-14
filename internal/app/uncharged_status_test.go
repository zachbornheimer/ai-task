package app_test

import (
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TestUnchargedFailuresAreNotShownAsFailed: a final run that failed
// without a strike (a timeout) leaves the task
// claimable as interrupted (attempt still open) or ready (released), with
// last_error kept and no [failed] annotation; verification_failed is only
// for a run charged against the attempt.
func TestUnchargedFailuresAreNotShownAsFailed(t *testing.T) {
	f := newGitFixture(t)
	f.setProofBudget(300 * time.Millisecond)
	notFailed := func(what string, id task.ID, want task.Status, errPart string) {
		t.Helper()
		v := f.show(string(id))
		if v.Status != want || v.Status.Annotation() == "[failed]" || v.Failures != 0 || !strings.Contains(v.LastError, errPart) {
			t.Fatalf("%s: status=%s failures=%d last_error=%q; want %s, no strike, last_error containing %q", what, v.Status, v.Failures, v.LastError, want, errPart)
		}
	}

	// A timeout, then the lease runs out: interrupted. Released instead: ready.
	a := f.addWith("slow", "sleep 1")
	s := f.claim(string(a))
	f.commit(s.Workspace, "a.txt", "a")
	_, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeCheckFailed)
	f.c.Advance(execution.DefaultLease + time.Minute)
	notFailed("timeout, lease expired", a, task.StatusInterrupted, "CHECK_FAILED")
	s2 := f.claim(string(a))
	if _, err := f.e.ReleaseTask(f.ctx, app.ReleaseRequest{Token: s2.Token}); err != nil {
		t.Fatal(err)
	}
	notFailed("timeout, released", a, task.StatusReady, "")

	// A failed proof is charged and still shows as failed.
	c := f.addWith("c", "false")
	sc := f.claim(string(c))
	f.commit(sc.Workspace, "c.txt", "c")
	_, err = f.e.Verify(f.ctx, sc.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeCheckFailed)
	if _, err := f.e.ReleaseTask(f.ctx, app.ReleaseRequest{Token: sc.Token}); err != nil {
		t.Fatal(err)
	}
	f.c.Advance(11 * time.Minute)
	if v := f.show(string(c)); v.Status != task.StatusVerificationFailed || v.Failures != 1 {
		t.Fatalf("a charged failure must stay verification_failed: status=%s failures=%d", v.Status, v.Failures)
	}
}
