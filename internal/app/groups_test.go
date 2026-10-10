package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TestEpicChecksRunWhenMembersComplete: a group with checks is verified
// once on the target branch after every member completes; it is complete
// only when its checks pass; a failure stalls the plan with the group's
// reason, a new member reopens the group, and the epic reruns after the
// member lands. Nothing counts against any task.
func TestEpicChecksRunWhenMembersComplete(t *testing.T) {
	f := newGitFixture(t)
	// The epic check wants both members' files on the target.
	res := f.apply(
		plan.AddGroup{Key: "identity", Title: "Identity", TaskChecks: []verification.CheckSpec{check("e2e", "test -f a.txt && test -f b.txt && test -f c.txt", true)}},
		plan.AddTask{Key: "a", Title: "a", Parent: "identity", TaskChecks: []verification.CheckSpec{check("unit-a", "test -f a.txt", true)}},
		plan.AddTask{Key: "b", Title: "b", Parent: "identity", TaskChecks: []verification.CheckSpec{check("unit-b", "test -f b.txt", true)}},
	)
	g, a, b := res.Created["identity"], res.Created["a"], res.Created["b"]
	if v := f.show(string(g)); len(v.Checks) != 1 || v.Size != task.SizeLarge || v.GroupVerification == nil || v.GroupVerification.Status != "" {
		t.Fatalf("group view: %+v", v)
	}
	// Members complete one by one; the epic is not complete yet.
	for _, id := range []task.ID{a, b} {
		s := f.claim(string(id))
		f.commit(s.Workspace, map[task.ID]string{a: "a.txt", b: "b.txt"}[id], "x")
		f.complete(s)
	}
	sum, _ := f.e.Summary(f.ctx, f.proj.ID)
	if sum.Done || sum.PendingEpics != 1 || sum.Active != 1 {
		t.Fatalf("members done, epic pending: %+v", sum)
	}
	if v := f.show(string(g)); v.GroupVerification.Status != "pending" {
		t.Fatalf("epic should be pending: %+v", v.GroupVerification)
	}
	// The epic fails: c.txt is missing. The plan stalls with the reason;
	// no task is charged.
	// Like cohort jobs, only internal errors surface; the outcome is on
	// the group.
	ran, err := f.e.RunPendingGroups(f.ctx, f.proj.ID, "")
	if !ran || err != nil {
		t.Fatalf("epic run: ran=%v err=%v", ran, err)
	}
	v := f.show(string(g))
	if v.GroupVerification.Status != "failed" || !strings.Contains(v.LastError, "c.txt") && !strings.Contains(v.LastError, "e2e") {
		t.Fatalf("after failure: %+v last_error=%q", v.GroupVerification, v.LastError)
	}
	sum, _ = f.e.Summary(f.ctx, f.proj.ID)
	if !sum.Stalled || sum.Done || len(sum.Reasons) == 0 || !strings.Contains(sum.Reasons[0], "epic") {
		t.Fatalf("stalled on the epic: %+v", sum)
	}
	if ran, _ := f.e.RunPendingGroups(f.ctx, f.proj.ID, ""); ran {
		t.Fatal("a failed epic must not rerun on the same revision without a change")
	}
	for _, id := range []task.ID{a, b} {
		if f.show(string(id)).Failures != 0 {
			t.Fatal("epic failure charged a member")
		}
	}
	// The planner adds the missing member: the group reopens, the member
	// lands, the epic reruns and passes; the group is complete.
	res = f.apply(plan.AddTask{Key: "c", Title: "c", Parent: "identity", TaskChecks: []verification.CheckSpec{check("unit-c", "test -f c.txt", true)}})
	c := res.Created["c"]
	if v := f.show(string(g)); v.GroupVerification.Status != "" {
		t.Fatalf("a new member must put the epic back to waiting: %+v", v.GroupVerification)
	}
	sc := f.claim(string(c))
	f.commit(sc.Workspace, "c.txt", "x")
	f.complete(sc)
	ran, err = f.e.RunPendingGroups(f.ctx, f.proj.ID, "")
	if !ran || err != nil {
		t.Fatalf("epic rerun: ran=%v err=%v", ran, err)
	}
	v = f.show(string(g))
	if v.GroupVerification.Status != "passed" || v.CompletedAt == nil {
		t.Fatalf("epic should be complete: %+v", v)
	}
	snap, _ := f.e.List(f.ctx, app.ListQuery{ProjectID: f.proj.ID, Filter: app.FilterAll})
	for _, gv := range snap.Groups {
		if gv.ID == g && !gv.Complete {
			t.Fatal("list must show the epic complete")
		}
	}
	sum, _ = f.e.Summary(f.ctx, f.proj.ID)
	if !sum.Done {
		t.Fatalf("done: %+v", sum)
	}
	// A verified epic whose checks change is verified again.
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Planner: true, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(g), TaskChecks: plan.Replace([]verification.CheckSpec{check("e2e", "test -f a.txt", true)})}}}); err != nil {
		t.Fatal(err)
	}
	if v := f.show(string(g)); v.CompletedAt != nil || v.GroupVerification.Status != "pending" {
		t.Fatalf("changed checks must reopen the epic: %+v", v.GroupVerification)
	}
	if _, err := os.Stat(filepath.Join(f.repo, "c.txt")); err != nil {
		t.Fatal("members' work must be on the target")
	}
	// Timeouts are reported as the epic's construction problem.
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Planner: true, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(g), Size: strptr("small"), TaskChecks: plan.Replace([]verification.CheckSpec{check("slow", "sleep 2", true)})}}}); err != nil {
		t.Fatal(err)
	}
	f.proj.Budgets.Small = 300 * 1e6
	if err := f.e.UpdateProject(f.ctx, f.proj); err != nil {
		t.Fatal(err)
	}
	if ran, err := f.e.RunPendingGroups(f.ctx, f.proj.ID, g); !ran || err != nil {
		t.Fatalf("epic timeout run: %v %v", ran, err)
	}
	if v := f.show(string(g)); v.GroupVerification.Status != "failed" || !strings.Contains(v.LastError, "budget") {
		t.Fatalf("epic timeout must be reported as the epic's construction problem: %+v %q", v.GroupVerification, v.LastError)
	}
	_ = fault.CodeInternal
}
