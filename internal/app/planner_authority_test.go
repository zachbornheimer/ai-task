package app_test

import (
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// Planner authority is the absence of the task's own token: no token and
// another task's token may rewrite a claimed task's proofs; its own token
// may not. --reset-attempts reports honestly and readies the task.
func TestPlannerAuthorityIsNoToken(t *testing.T) {
	f := newFixture(t)
	a := f.add("a")
	b := f.add("b")
	sa := f.claim(string(a))
	sb := f.claim(string(b))
	edit := func(session string, id string) error {
		_, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Session: session, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(id), TaskChecks: plan.Replace([]verification.CheckSpec{check("u", "test -d .", true)})}}})
		return err
	}
	if err := edit("", string(a)); err != nil {
		t.Fatalf("no token: %v", err)
	}
	wantCode(t, edit(string(sa.Token), string(a)), fault.CodeInvalidSession)
	if err := edit(string(sb.Token), string(a)); err != nil {
		t.Fatalf("other task's token: %v", err)
	}

	f.e.Release(f.ctx, app.ReleaseRequest{Token: sb.Token})
	c := f.add("c")
	s := f.claim(string(c))
	f.c.Advance(5 * time.Hour)
	if st := f.status(string(c)); st != task.StatusInterrupted {
		t.Fatalf("want interrupted, got %s", st)
	}
	_ = s
	reset := func() plan.Result {
		r, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(c), ResetAttempts: true}}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := reset(); r.Changed != 1 {
		t.Fatalf("first reset changed=%d", r.Changed)
	}
	if st := f.status(string(c)); st != task.StatusReady {
		t.Fatalf("want ready, got %s", st)
	}
	if r := reset(); r.Changed != 0 {
		t.Fatalf("second reset changed=%d", r.Changed)
	}
}
