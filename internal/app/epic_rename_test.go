package app_test

import (
	"testing"

	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TestRenamingVerifiedEpicKeepsIt: only a change to a verified epic's
// checks or size withdraws its completion; renaming it or moving it under
// another group does not.
func TestRenamingVerifiedEpicKeepsIt(t *testing.T) {
	f := newGitFixture(t)
	res := f.apply(
		plan.AddGroup{Key: "outer", Title: "Outer"},
		plan.AddGroup{Key: "epic", Title: "Epic", TaskChecks: []verification.CheckSpec{check("e2e", "test -f a.txt", true)}},
		plan.AddTask{Key: "a", Title: "a", Parent: "epic", TaskChecks: []verification.CheckSpec{check("unit-a", "test -f a.txt", true)}},
	)
	g, a := res.Created["epic"], res.Created["a"]
	s := f.claim(string(a))
	f.commit(s.Workspace, "a.txt", "x")
	f.complete(s)
	if ran, err := f.e.RunPendingGroups(f.ctx, f.proj.ID, ""); !ran || err != nil {
		t.Fatalf("epic run: ran=%v err=%v", ran, err)
	}
	if v := f.show(string(g)); v.CompletedAt == nil {
		t.Fatalf("epic should be complete: %+v", v)
	}
	update := func(u plan.UpdateTask) {
		t.Helper()
		u.Target = plan.Ref(g)
		f.apply(u)
	}
	title := "Renamed epic"
	update(plan.UpdateTask{Title: &title})
	if v := f.show(string(g)); v.CompletedAt == nil || v.Title != title {
		t.Fatalf("renaming must keep the epic verified: completed=%v title=%q", v.CompletedAt, v.Title)
	}
	parent := plan.Ref("outer")
	update(plan.UpdateTask{Parent: &parent})
	if v := f.show(string(g)); v.CompletedAt == nil {
		t.Fatalf("moving the epic must keep it verified: %+v", v)
	}
	// A change to the checks still withdraws completion.
	update(plan.UpdateTask{TaskChecks: plan.Replace([]verification.CheckSpec{check("e2e", "test -f a.txt && true", true)})})
	if v := f.show(string(g)); v.CompletedAt != nil {
		t.Fatalf("changed checks must reopen the epic: %+v", v)
	}
}
