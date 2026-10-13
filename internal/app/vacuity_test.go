package app_test

import (
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TestVacuityRejectsOnAddAndUpdate: one rule guards both write paths and the
// error names the rule.
func TestVacuityRejectsOnAddAndUpdate(t *testing.T) {
	f := newGitFixture(t)
	vacuous := []verification.CheckSpec{check("v", "true", true)}
	_, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
		plan.AddTask{Key: "a", Title: "a", TaskChecks: vacuous},
	}})
	if fault.CodeOf(err) != fault.CodeInvalidInput || !strings.Contains(err.Error(), "no-op") {
		t.Fatalf("add: want INVALID_INPUT naming no-op, got %v", err)
	}
	f.apply(plan.AddTask{Key: "b", Title: "b", TaskChecks: okChecks()})
	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Planner: true, Operations: []plan.Change{
		plan.UpdateTask{Target: plan.Ref("b"), TaskChecks: plan.Patch[verification.CheckSpec]{Set: true, Value: vacuous}},
	}})
	if fault.CodeOf(err) != fault.CodeInvalidInput || !strings.Contains(err.Error(), "no-op") {
		t.Fatalf("update: want INVALID_INPUT naming no-op, got %v", err)
	}
}
