package app_test

import (
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func TestTaskRequiresRegressionAndCompletion(t *testing.T) {
	f := newFixture(t)
	_, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
		plan.AddTask{Key: "only-c", Title: "only-c", TaskChecks: okChecks()},
	}})
	if fault.CodeOf(err) != fault.CodeMissingVerification {
		t.Fatalf("no regression: %v", err)
	}
	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
		plan.AddTask{Key: "only-r", Title: "only-r", Regression: okChecks()},
	}})
	if fault.CodeOf(err) != fault.CodeMissingVerification {
		t.Fatalf("no task checks: %v", err)
	}
	res, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
		plan.AddTask{Key: "both", Title: "both", TaskChecks: okChecks(), Regression: []verification.CheckSpec{check("r", "true", true)}, RegressionSize: "medium"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := sqlite.Open(f.ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	id := res.Created["both"]
	var rec sqlite.Record
	if err := st.Read(f.ctx, func(tx *sqlite.Tx) error {
		var e error
		rec, e = tx.GetRecord(id)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if len(rec.Task.Verification.Regression) != 1 || rec.Task.Verification.Regression[0].Size != "medium" {
		t.Fatalf("regression size not stored on the task: %+v", rec.Task.Verification.Regression)
	}
}
