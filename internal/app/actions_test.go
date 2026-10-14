package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/proof"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func action(t *testing.T, s string, detach bool) proof.ActionSpec {
	t.Helper()
	p, err := proof.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return proof.ActionSpec{Preset: p.Preset, Subject: p.Subject, Options: p.Options, Detach: detach}
}

func (f *fixture) addWithActions(key string, actions ...proof.ActionSpec) task.ID {
	f.t.Helper()
	res := f.apply(plan.AddTask{Key: key, Title: key, TaskChecks: okChecks(), OnComplete: actions})
	return res.Created[key]
}

func (f *fixture) actions(ref string) []proof.ActionResult {
	f.t.Helper()
	v, err := f.e.Show(f.ctx, f.proj.ID, ref, true)
	if err != nil {
		f.t.Fatal(err)
	}
	return v.Actions
}

func TestVerifyCompleteRunsActions(t *testing.T) {
	f := newGitFixture(t)

	// Waited and detached actions run after atomic completion.
	id := f.addWithActions("ok",
		action(t, "run: sh -c 'echo done > out.txt'", false),
		action(t, "run: sleep 30", true))
	s := f.claim(string(id))
	res := f.complete(s)
	if f.status(string(id)) != task.StatusComplete {
		t.Fatalf("task must be complete")
	}
	acts := f.actions(string(id))
	if len(acts) != 2 || len(res.Actions) != 2 {
		t.Fatalf("want 2 actions, show=%d result=%d", len(acts), len(res.Actions))
	}
	waited, detached := acts[0], acts[1]
	if waited.Status != proof.ActionSucceeded || waited.Run == nil || waited.Run.ReturnCode == nil || *waited.Run.ReturnCode != 0 ||
		waited.Run.ElapsedMS <= 0 || waited.Run.CheckType != proof.CheckOnComplete || waited.Attempts != 1 {
		t.Fatalf("waited action wrong: %+v run=%+v", waited, waited.Run)
	}
	if detached.Status != proof.ActionDetached || detached.Run == nil || detached.Run.StartedAt.IsZero() || detached.Run.ReturnCode != nil {
		t.Fatalf("detached action wrong: %+v run=%+v", detached, detached.Run)
	}
	if b, err := os.ReadFile(filepath.Join(s.Workspace, "out.txt")); err != nil || strings.TrimSpace(string(b)) != "done" {
		t.Fatalf("out.txt missing in the worktree: %q %v", b, err)
	}

	// A failing waited action never undoes completion; a repeat verify
	// complete retries only the failed action.
	id2 := f.addWithActions("flaky",
		action(t, "run: sh -c 'echo x >> count.txt'", false),
		action(t, "run: sh -c 'test -f marker.txt'", false))
	s2 := f.claim(string(id2))
	f.complete(s2)
	if f.status(string(id2)) != task.StatusComplete {
		t.Fatalf("a failing action must not undo completion")
	}
	acts = f.actions(string(id2))
	if acts[0].Status != proof.ActionSucceeded || acts[1].Status != proof.ActionFailed {
		t.Fatalf("want succeeded+failed, got %s+%s", acts[0].Status, acts[1].Status)
	}
	if err := os.WriteFile(filepath.Join(s2.Workspace, "marker.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := f.e.Verify(f.ctx, s2.Token, verification.ModeComplete)
	if err != nil || !again.Completed {
		t.Fatalf("repeat verify complete: %+v %v", again, err)
	}
	acts = f.actions(string(id2))
	if acts[0].Status != proof.ActionSucceeded || acts[0].Attempts != 1 {
		t.Fatalf("succeeded action must not re-run: %+v", acts[0])
	}
	if acts[1].Status != proof.ActionSucceeded || acts[1].Attempts != 2 {
		t.Fatalf("failed action must be retried once: %+v", acts[1])
	}
	if b, _ := os.ReadFile(filepath.Join(s2.Workspace, "count.txt")); strings.Count(string(b), "x") != 1 {
		t.Fatalf("succeeded action ran again: %q", b)
	}

	// --reverify appends RunResults and leaves the task complete.
	before := len(f.runResults(string(id2)))
	rv, err := f.e.Reverify(f.ctx, s2.Token)
	if err != nil || !rv.Passed {
		t.Fatalf("reverify: %+v %v", rv, err)
	}
	if after := len(f.runResults(string(id2))); after <= before {
		t.Fatalf("reverify must append RunResults: %d -> %d", before, after)
	}
	if f.status(string(id2)) != task.StatusComplete {
		t.Fatalf("reverify must leave the task complete")
	}
	if a := f.actions(string(id2)); a[0].Attempts != 1 || a[1].Attempts != 2 {
		t.Fatalf("reverify must not re-run actions: %+v", a)
	}
}
