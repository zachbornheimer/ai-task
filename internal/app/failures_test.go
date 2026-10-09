package app_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// Only a genuine failed proof counts against a task, at most once per
// attempt and only for the current attempt; environment and authority
// problems are recorded, never counted; a double-fired completion is
// refused rather than raced.
func TestFailureAccountingIsFencedAndClassified(t *testing.T) {
	f := newGitFixture(t)
	p, _ := f.e.Project(f.ctx, f.proj.ID)
	p.RetryCooldown = 10 * time.Minute
	if err := f.e.UpdateProject(f.ctx, p); err != nil {
		t.Fatal(err)
	}
	failures := func(id task.ID) int { return f.show(string(id)).Failures }

	// 1. A failed proof counts once per attempt, however often it is retried
	//    within the attempt, and a later release --failed of the same attempt
	//    adds nothing.
	a := f.addWith("a", "false")
	s := f.claim(string(a))
	f.commit(s.Workspace, "a.txt", "a")
	_, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeVerificationFailed)
	_, err = f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeVerificationFailed)
	if failures(a) != 1 {
		t.Fatalf("failures after two failed proofs in one attempt: %d", failures(a))
	}
	if _, err := f.e.Release(f.ctx, s.Token, app.ReleaseOptions{Failed: true}); err != nil {
		t.Fatal(err)
	}
	if failures(a) != 1 {
		t.Fatalf("release --failed double-counted: %d", failures(a))
	}
	f.c.Advance(11 * time.Minute)
	s2 := f.claim(string(a))
	_, err = f.e.Verify(f.ctx, s2.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeVerificationFailed)
	if failures(a) != 2 {
		t.Fatalf("second attempt's failure not counted: %d", failures(a))
	}

	// 2. The environment stopping the run (an untracked file in the target
	//    checkout at a path the promotion would create) is not a strike: it
	//    is recorded as last_error and cleared on completion.
	b := f.add("b")
	sb := f.claim(string(b))
	f.commit(sb.Workspace, "b.txt", "b")
	scratch := filepath.Join(f.repo, "b.txt")
	os.WriteFile(scratch, []byte("local draft"), 0o644)
	_, err = f.e.Verify(f.ctx, sb.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeIntegrationFailed)
	v := f.show(string(b))
	if v.Failures != 0 || v.LastError == "" || v.Status != task.StatusClaimed {
		t.Fatalf("environment failure misclassified: failures=%d last_error=%q status=%s", v.Failures, v.LastError, v.Status)
	}
	os.Remove(scratch)
	f.complete(sb)
	if v := f.show(string(b)); v.LastError != "" || v.Failures != 0 {
		t.Fatalf("completion did not clear last_error: %+v", v)
	}

	// 3. A double-fired `verify complete` is refused while its twin runs and
	//    replays the stored result afterwards; nothing is counted.
	flag := filepath.Join(t.TempDir(), "go")
	c := f.addWith("c", "while [ ! -f "+flag+" ]; do sleep 0.05; done")
	sc := f.claim(string(c))
	f.commit(sc.Workspace, "c.txt", "c")
	done := make(chan error, 1)
	go func() { _, err := f.e.Verify(f.ctx, sc.Token, verification.ModeComplete); done <- err }()
	f.waitStatus(string(c), task.StatusVerifying)
	_, err = f.e.Verify(f.ctx, sc.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeVerificationRunning)
	os.WriteFile(flag, []byte("1"), 0o644)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	res, err := f.e.Verify(f.ctx, sc.Token, verification.ModeComplete)
	if err != nil || !res.Replayed || !res.Completed {
		t.Fatalf("replay: %+v %v", res, err)
	}
	if v := f.show(string(c)); v.Failures != 0 || v.Status != task.StatusComplete {
		t.Fatalf("duplicate completion counted a failure: %+v", v)
	}

	// 4. A superseded attempt cannot count a failure against its successor.
	d := f.add("d")
	sd := f.claim(string(d))
	f.c.Advance(execution.DefaultLease + time.Minute)
	sd2 := f.claim(string(d))
	_, err = f.e.Release(f.ctx, sd.Token, app.ReleaseOptions{Failed: true})
	wantCode(t, err, fault.CodeSessionSuperseded)
	if failures(d) != 0 {
		t.Fatalf("stale release counted: %d", failures(d))
	}
	if _, err := f.e.Release(f.ctx, sd2.Token, app.ReleaseOptions{Failed: true}); err != nil {
		t.Fatal(err)
	}
	if failures(d) != 1 {
		t.Fatalf("current attempt's failure not counted: %d", failures(d))
	}
}
