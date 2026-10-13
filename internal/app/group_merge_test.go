package app_test

import (
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TestGroupVerifiesOnChildrenMerge: a group's proof runs on the merge of
// every child's completed revision (no integration branch), the group is
// not ready until every child is complete, and children that cannot be
// merged fail the group with BASE_CONFLICT naming the files.
func TestGroupVerifiesOnChildrenMerge(t *testing.T) {
	t.Run("proof runs on the merge once every child is complete", func(t *testing.T) {
		f := stackedFixture(t)
		res := f.apply(
			plan.AddGroup{Key: "g", Title: "g", TaskChecks: []verification.CheckSpec{check("merged", "test -f a.txt && test -f b.txt", true)}},
			plan.AddTask{Key: "a", Title: "a", Parent: "g", TaskChecks: []verification.CheckSpec{check("unit-a", "test -d .", true)}},
			plan.AddTask{Key: "b", Title: "b", Parent: "g", TaskChecks: []verification.CheckSpec{check("unit-b", "test -d .", true)}},
		)
		g, a, b := res.Created["g"], res.Created["a"], res.Created["b"]
		f.completeWith(a, "a.txt", "from a")
		_, err := f.e.VerifyGroup(f.ctx, f.proj.ID, string(g))
		wantCode(t, err, fault.CodeNotReady)
		f.completeWith(b, "b.txt", "from b")
		out, err := f.e.VerifyGroup(f.ctx, f.proj.ID, string(g))
		if err != nil || !out.Completed || !out.Passed {
			t.Fatalf("group verify: %+v %v", out, err)
		}
		if len(out.Evidence) == 0 || out.Evidence[0].Run == nil {
			t.Fatalf("no RunResult recorded: %+v", out.Evidence)
		}
		if v := f.show(string(g)); v.CompletedAt == nil {
			t.Fatalf("group not complete: %+v", v)
		}
	})

	t.Run("conflicting children fail with BASE_CONFLICT", func(t *testing.T) {
		f := stackedFixture(t)
		res := f.apply(
			plan.AddGroup{Key: "g", Title: "g", TaskChecks: []verification.CheckSpec{check("any", "test -d .", true)}},
			plan.AddTask{Key: "a", Title: "a", Parent: "g", TaskChecks: []verification.CheckSpec{check("unit-a", "test -d .", true)}},
			plan.AddTask{Key: "b", Title: "b", Parent: "g", TaskChecks: []verification.CheckSpec{check("unit-b", "test -d .", true)}},
		)
		g, a, b := res.Created["g"], res.Created["a"], res.Created["b"]
		f.completeWith(a, "shared.txt", "line from a")
		f.completeWith(b, "shared.txt", "line from b")
		_, err := f.e.VerifyGroup(f.ctx, f.proj.ID, string(g))
		wantCode(t, err, fault.CodeBaseConflict)
		if !strings.Contains(fault.MessageOf(err), "shared.txt") {
			t.Fatalf("conflict does not name the file: %v", err)
		}
		if v := f.show(string(g)); v.CompletedAt != nil {
			t.Fatalf("conflicted group completed: %+v", v)
		}
	})
}
