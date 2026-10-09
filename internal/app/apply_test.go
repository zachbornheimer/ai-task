package app_test

import (
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func count(f *fixture, filter app.Filter) int {
	f.t.Helper()
	snap, err := f.e.List(f.ctx, app.ListQuery{ProjectID: f.proj.ID, Filter: filter})
	if err != nil {
		f.t.Fatal(err)
	}
	return len(snap.Tasks) + len(snap.Groups)
}

func TestAtomicProposalInsertsNothingOnInvalidEdge(t *testing.T) {
	f := newFixture(t)
	a := f.add("a")
	// Unknown prerequisite: the whole set is rejected.
	_, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
		plan.AddTask{Key: "b", Title: "b"},
		plan.AddTask{Key: "c", Title: "c", Requires: []plan.Ref{"nope"}},
	}})
	wantCode(t, err, fault.CodeNotFound)
	if count(f, app.FilterAll) != 1 {
		t.Fatal("partial insert")
	}
	// Cycle inside the set: nothing changes.
	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
		plan.AddTask{Key: "y", Title: "y", Requires: []plan.Ref{plan.Ref(a)}},
		plan.AddTask{Key: "x", Title: "x", Requires: []plan.Ref{"y"}},
		plan.UpdateTask{Target: plan.Ref(a), AddRequires: []plan.Ref{"x"}},
	}})
	wantCode(t, err, fault.CodeDependencyCycle)
	if count(f, app.FilterAll) != 1 {
		t.Fatal("partial insert after cycle")
	}
	v := f.show(string(a))
	if len(v.Requires) != 0 || v.ContractRev != 1 {
		t.Fatalf("a touched: %+v", v)
	}
	// Plan revision did not advance on failure.
	p, _ := f.e.Project(f.ctx, f.proj.ID)
	if p.PlanRev != 1 {
		t.Fatalf("plan rev %d", p.PlanRev)
	}
	// Groups cannot be prerequisites or claim targets.
	g := f.apply(plan.AddGroup{Key: "g", Title: "G"}).Created["g"]
	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.AddTask{Title: "t", Requires: []plan.Ref{"g"}}}})
	wantCode(t, err, fault.CodeInvalidInput)
	_, err = f.e.Claim(f.ctx, app.ClaimRequest{TaskID: &g})
	wantCode(t, err, fault.CodeInvalidInput)
}

func TestStableKeysAndIdempotency(t *testing.T) {
	f := newFixture(t)
	op := plan.AddTask{Key: "oauth", Title: "OAuth endpoint", Acceptance: []string{"400 on bad code"}, TaskChecks: []verification.CheckSpec{check("unit", "true", true)}}
	r1, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, IdempotencyKey: "plan-1", Operations: []plan.Change{op}})
	if err != nil || r1.Changed != 1 {
		t.Fatalf("%+v %v", r1, err)
	}
	// Idempotency key replay: stored result, nothing applied.
	r2, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, IdempotencyKey: "plan-1", Operations: []plan.Change{op}})
	if err != nil || !r2.Replayed || r2.Created["oauth"] != r1.Created["oauth"] || r2.PlanRev != r1.PlanRev {
		t.Fatalf("%+v %v", r2, err)
	}
	// Same key, identical definition, new idempotency key: no duplicate.
	r3, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{op}})
	if err != nil || r3.Changed != 0 || count(f, app.FilterAll) != 1 {
		t.Fatalf("%+v %v", r3, err)
	}
	// Same key, conflicting definition: conflict, no overwrite.
	changed := op
	changed.Title = "OAuth endpoint v2"
	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{changed}})
	wantCode(t, err, fault.CodeDuplicateKey)
	if f.show("oauth").Title != "OAuth endpoint" {
		t.Fatal("overwritten")
	}
	// Keys resolve within the same set and from the store.
	r4 := f.apply(plan.AddTask{Key: "dep", Title: "dep", Requires: []plan.Ref{"oauth"}}, plan.AddTask{Key: "dep2", Title: "dep2", Requires: []plan.Ref{"dep"}})
	if len(r4.Created) != 2 || f.status("dep2") != task.StatusBlocked {
		t.Fatalf("%+v", r4)
	}
}

func TestReviewMutationIsOneRevisionWithOptimisticConcurrency(t *testing.T) {
	f := newFixture(t)
	a, b := f.add("a"), f.add("b", "a")
	snap, _ := f.e.List(f.ctx, app.ListQuery{ProjectID: f.proj.ID, Filter: app.FilterAll})
	// A reviewer splits a into a1/a2, repoints b, archives a: one revision.
	res, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, ExpectedPlanRev: snap.Revision, Operations: []plan.Change{
		plan.AddTask{Key: "a1", Title: "a part 1", TaskChecks: []verification.CheckSpec{check("u1", "true", true)}},
		plan.AddTask{Key: "a2", Title: "a part 2", Requires: []plan.Ref{"a1"}, TaskChecks: []verification.CheckSpec{check("u2", "true", true)}},
		plan.UpdateTask{Target: plan.Ref(b), Requires: plan.Replace([]plan.Ref{"a2"})},
		plan.ArchiveTask{Target: plan.Ref(a)},
	}})
	if err != nil || res.Changed != 4 || res.PlanRev != snap.Revision+1 {
		t.Fatalf("%+v %v", res, err)
	}
	if v := f.show(string(b)); len(v.Requires) != 1 || v.Requires[0].Key != "a2" {
		t.Fatalf("b requires: %+v", v.Requires)
	}
	if f.status(string(a)) != task.StatusArchived {
		t.Fatal("a not archived")
	}
	// Stale revision is rejected and nothing changes.
	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, ExpectedPlanRev: snap.Revision, Operations: []plan.Change{plan.AddTask{Key: "late", Title: "late"}}})
	wantCode(t, err, fault.CodePlanConflict)
	if _, err := f.e.Show(f.ctx, f.proj.ID, "late", false); !fault.Is(err, fault.CodeNotFound) {
		t.Fatal("late created despite conflict")
	}
	// Archiving a task that others require, without repairing, is rejected.
	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.ArchiveTask{Target: "a2"}}})
	wantCode(t, err, fault.CodePlanConflict)
	// Repairing in the same set works.
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
		plan.UpdateTask{Target: plan.Ref(b), RemoveRequires: []plan.Ref{"a2"}},
		plan.ArchiveTask{Target: "a2"},
	}}); err != nil {
		t.Fatal(err)
	}
	// Updates distinguish unchanged / set / clear.
	f.apply(plan.UpdateTask{Target: plan.Ref(b), Outcome: strptr("new outcome"), Constraints: plan.Replace([]string{"c1"})})
	v := f.show(string(b))
	if v.Outcome != "new outcome" || len(v.Constraints) != 1 || v.Title != "b" || v.ContractRev != 2 {
		t.Fatalf("%+v", v)
	}
	f.apply(plan.UpdateTask{Target: plan.Ref(b), Constraints: plan.Replace([]string{})})
	if v := f.show(string(b)); len(v.Constraints) != 0 {
		t.Fatal("clear")
	}
	// No status setters exist: the public patch has no such fields; derived
	// status cannot be set. (Compile-time: plan.UpdateTask has no Status.)
}

func TestGroupsAreOrganizationalOnly(t *testing.T) {
	f := newFixture(t)
	res := f.apply(
		plan.AddGroup{Key: "identity", Title: "Identity Core"},
		plan.AddTask{Key: "t1", Title: "t1", Parent: "identity", TaskChecks: []verification.CheckSpec{check("u", "true", true)}},
		plan.AddTask{Key: "t2", Title: "t2", Parent: "identity", Requires: []plan.Ref{"t1"}, TaskChecks: []verification.CheckSpec{check("u", "true", true)}},
		plan.AddGroup{Key: "sub", Title: "Sub", Parent: "identity"},
		plan.AddTask{Key: "t3", Title: "t3", Parent: "sub"},
	)
	g := f.show(string(res.Created["identity"]))
	if g.Kind != task.KindGroup || g.Status != task.StatusGroup || g.Progress == nil || g.Progress.Total != 3 || g.Progress.Complete != 0 {
		t.Fatalf("%+v", g)
	}
	// Membership never implies a dependency: t1 is ready, t2 blocked by its
	// explicit edge only, t3 ready.
	if f.status("t1") != task.StatusReady || f.status("t2") != task.StatusBlocked || f.status("t3") != task.StatusReady {
		t.Fatal("membership leaked into eligibility")
	}
	// Empty group is 0/0 and not complete.
	empty := f.apply(plan.AddGroup{Key: "empty", Title: "Empty"}).Created["empty"]
	snap, _ := f.e.List(f.ctx, app.ListQuery{ProjectID: f.proj.ID, Filter: app.FilterAll})
	for _, gv := range snap.Groups {
		if gv.ID == empty && (gv.Complete || gv.Progress.Total != 0) {
			t.Fatalf("empty group: %+v", gv)
		}
	}
	// A group cannot be nested in itself; a group with members cannot be
	// archived without them.
	_, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.UpdateTask{Target: "identity", Parent: refptr("sub")}}})
	wantCode(t, err, fault.CodeDependencyCycle)
	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.ArchiveTask{Target: "sub"}}})
	wantCode(t, err, fault.CodePlanConflict)
}

func refptr(s string) *plan.Ref { r := plan.Ref(s); return &r }

func TestRegressionNamespaceIsSeparate(t *testing.T) {
	f := newFixture(t)
	f.setRegression(check("lint", "true", true))
	_, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.AddTask{Title: "x", TaskChecks: []verification.CheckSpec{check("lint", "exit 1", true)}}}})
	wantCode(t, err, fault.CodeInvalidInput)
	f.add("ok")
	p, _ := f.e.Project(f.ctx, f.proj.ID)
	p.Regression = []verification.CheckSpec{check("unit-ok", "true", true)}
	wantCode(t, f.e.UpdateProject(f.ctx, p), fault.CodeInvalidInput)
}

func TestDiscoveredBlockerNeedsSessionOrPlanner(t *testing.T) {
	f := newFixture(t)
	c := f.add("c")
	s := f.claim(string(c))
	// Without authority: the claimed task's eligibility cannot change.
	_, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.AddTask{Key: "x", Title: "x", Blocks: []plan.Ref{plan.Ref(c)}}}})
	wantCode(t, err, fault.CodePlanConflict)
	// With the holder's session: allowed; c stays claimed (claim outranks
	// blocked) and becomes blocked once released.
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Session: string(s.Token), Operations: []plan.Change{plan.AddTask{Key: "x", Title: "x", Blocks: []plan.Ref{plan.Ref(c)}}}}); err != nil {
		t.Fatal(err)
	}
	if f.status(string(c)) != task.StatusClaimed {
		t.Fatal("claimed task hidden")
	}
	if _, err := f.e.Release(f.ctx, s.Token, app.ReleaseOptions{Note: "needs x"}); err != nil {
		t.Fatal(err)
	}
	if f.status(string(c)) != task.StatusBlocked {
		t.Fatal("not blocked after release")
	}
	// A different session cannot do it; the planner flag can.
	d := f.add("d")
	sd := f.claim(string(d))
	other := f.claim("x")
	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Session: string(other.Token), Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(d), AddRequires: []plan.Ref{"x"}}}})
	wantCode(t, err, fault.CodeSessionSuperseded)
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Planner: true, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(d), AddRequires: []plan.Ref{"x"}}}}); err != nil {
		t.Fatal(err)
	}
	// Contract edits to a claimed task need the planner, never a session.
	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Session: string(sd.Token), Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(d), Title: strptr("renamed")}}})
	wantCode(t, err, fault.CodePlanConflict)
}
