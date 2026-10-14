package app_test

import (
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/proof"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func goCheck(id string, argv ...string) verification.CheckSpec {
	return verification.CheckSpec{ID: id, Command: argv, Required: true}
}

func wantSmallSizeReject(t *testing.T, err error) {
	t.Helper()
	if fault.CodeOf(err) != fault.CodeInvalidInput || !strings.Contains(err.Error(), "-run") || !strings.Contains(err.Error(), "size=") {
		t.Fatalf("want INVALID_INPUT naming -run and --size, got %v", err)
	}
}

// TestSmallSizeRunFilterAllowsNarrowGoTest: a small task's check that
// already filters with -run is accepted (even with ./...); go test
// without -run is rejected on add and on update of checks or size.
func TestSmallSizeRunFilterAllowsNarrowGoTest(t *testing.T) {
	f := newGitFixture(t)

	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
		plan.AddTask{Key: "filtered", Title: "filtered", TaskChecks: []verification.CheckSpec{goCheck("a", "go", "test", "./...", "-run", "TestX")}},
		plan.AddTask{Key: "filtered-sh", Title: "filtered-sh", TaskChecks: []verification.CheckSpec{goCheck("b", "sh", "-c", "go test ./pkg -run '^TestX$' -v")}},
	}}); err != nil {
		t.Fatalf("narrowed go test: %v", err)
	}
	_, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
		plan.AddTask{Key: "wide", Title: "wide", TaskChecks: []verification.CheckSpec{goCheck("c", "go", "test", "./...")}},
	}})
	wantSmallSizeReject(t, err)
	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
		plan.AddTask{Key: "pkg", Title: "pkg", TaskChecks: []verification.CheckSpec{goCheck("d", "go", "test", "./pkg")}},
	}})
	wantSmallSizeReject(t, err)

	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
		plan.UpdateTask{Target: plan.Ref("filtered"), TaskChecks: plan.Patch[verification.CheckSpec]{Set: true, Value: []verification.CheckSpec{goCheck("a", "go", "test", "./...")}}},
	}})
	wantSmallSizeReject(t, err)

	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
		plan.UpdateTask{Target: plan.Ref("filtered"), TaskChecks: plan.Patch[verification.CheckSpec]{Set: true, Value: []verification.CheckSpec{goCheck("a", "go", "test", "./...", "-run", "TestY")}}},
	}}); err != nil {
		t.Fatalf("filtered update: %v", err)
	}

	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
		plan.AddTask{Key: "pkg", Title: "pkg", Proof: &proof.ProofSpec{Preset: "run", Subject: "go test ./...", Options: map[string]string{}, Size: proof.SizeLarge}},
	}}); err != nil {
		t.Fatalf("large add: %v", err)
	}
}
