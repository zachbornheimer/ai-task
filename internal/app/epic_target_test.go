package app_test

import (
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TestEpicReportsUnresolvableTarget: when the target branch cannot be
// resolved, a ready epic is not shown as pending: claiming it fails with
// an error and its status says why, and the plan stalls on that reason.
func TestEpicReportsUnresolvableTarget(t *testing.T) {
	f := newGitFixture(t)
	res := f.apply(
		plan.AddGroup{Key: "epic", Title: "Epic", TaskChecks: []verification.CheckSpec{check("e2e", "test -f a.txt", true)}},
		plan.AddTask{Key: "a", Title: "a", Parent: "epic", TaskChecks: []verification.CheckSpec{check("unit-a", "test -f a.txt", true)}},
	)
	g, a := res.Created["epic"], res.Created["a"]
	s := f.claim(string(a))
	f.commit(s.Workspace, "a.txt", "x")
	f.complete(s)
	if v := f.show(string(g)); v.GroupVerification.Status != "pending" {
		t.Fatalf("epic should be pending while the target resolves: %+v", v.GroupVerification)
	}

	f.git(f.repo, "update-ref", "-d", "refs/heads/"+f.proj.TargetBranch)

	ran, err := f.e.RunPendingGroups(f.ctx, f.proj.ID, "")
	if ran || err == nil {
		t.Fatalf("claiming an epic without a target must fail: ran=%v err=%v", ran, err)
	}
	gv := f.show(string(g)).GroupVerification
	if gv.Status == "pending" || gv.Status == "" || !strings.Contains(gv.Summary, f.proj.TargetBranch) {
		t.Fatalf("status must say the target cannot be resolved, not pending: %+v", gv)
	}
	sum, err := f.e.Summary(f.ctx, f.proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Done || !sum.Stalled || len(sum.Reasons) == 0 || !strings.Contains(strings.Join(sum.Reasons, " "), f.proj.TargetBranch) {
		t.Fatalf("summary must stall on the unresolvable target: %+v", sum)
	}
}
