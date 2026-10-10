package app_test

import (
	"testing"

	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TestNestedEpicWaitsThroughPlainGroups: an epic is not ready while any
// epic beneath it, even through plain groups, is unverified, so an outer
// epic can never pass over a failed inner epic.
func TestNestedEpicWaitsThroughPlainGroups(t *testing.T) {
	f := newGitFixture(t)
	res := f.apply(
		plan.AddGroup{Key: "a", Title: "A", TaskChecks: []verification.CheckSpec{check("outer", "true", true)}},
		plan.AddGroup{Key: "b", Title: "B", Parent: "a"},
		plan.AddGroup{Key: "c", Title: "C", Parent: "b", TaskChecks: []verification.CheckSpec{check("inner", "false", true)}},
		plan.AddTask{Key: "t", Title: "t", Parent: "c", TaskChecks: []verification.CheckSpec{check("unit", "test -f t.txt", true)}},
	)
	a, c, tt := res.Created["a"], res.Created["c"], res.Created["t"]
	s := f.claim(string(tt))
	f.commit(s.Workspace, "t.txt", "x")
	f.complete(s)
	for i := 0; i < 2; i++ {
		if _, err := f.e.RunPendingGroups(f.ctx, f.proj.ID, ""); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if v := f.show(string(c)); v.GroupVerification == nil || v.GroupVerification.Status != "failed" {
		t.Fatalf("inner epic should be failed: %+v", v.GroupVerification)
	}
	if v := f.show(string(a)); v.CompletedAt != nil {
		t.Fatalf("outer epic must not complete over a failed inner epic: %+v", v.GroupVerification)
	}
}
