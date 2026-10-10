package app_test

import (
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func goCheck(id string, argv ...string) verification.CheckSpec {
	return verification.CheckSpec{ID: id, Command: argv, Required: true}
}

func hasWholeSuite(w []string) bool {
	return len(w) > 0 && strings.Contains(strings.Join(w, "\n"), "whole-suite")
}

// TestWholeSuiteWarningRespectsRunFilter: a small task's check that
// already filters tests with -run is not a whole-suite run (even with
// ./...); one that does not is warned about, when the task is added and
// also when an update sets its checks or size.
func TestWholeSuiteWarningRespectsRunFilter(t *testing.T) {
	f := newGitFixture(t)

	res := f.apply(
		plan.AddTask{Key: "filtered", Title: "filtered", TaskChecks: []verification.CheckSpec{goCheck("a", "go", "test", "./...", "-run", "TestX")}},
		plan.AddTask{Key: "filtered-sh", Title: "filtered-sh", TaskChecks: []verification.CheckSpec{goCheck("b", "sh", "-c", "go test ./pkg -run '^TestX$' -v")}},
		plan.AddTask{Key: "wide", Title: "wide", TaskChecks: []verification.CheckSpec{goCheck("c", "go", "test", "./...")}},
		plan.AddTask{Key: "pkg", Title: "pkg", TaskChecks: []verification.CheckSpec{goCheck("d", "go", "test", "./pkg")}},
	)
	for _, key := range []string{"filtered", "filtered-sh"} {
		if len(res.Warnings[key]) != 0 {
			t.Errorf("%s: a -run filter must not warn: %v", key, res.Warnings[key])
		}
	}
	for _, key := range []string{"wide", "pkg"} {
		if !hasWholeSuite(res.Warnings[key]) {
			t.Errorf("%s: expected a whole-suite warning: %v", key, res.Warnings)
		}
	}

	// Updates warn too: new checks on a small task...
	res = f.apply(plan.UpdateTask{Target: plan.Ref("filtered"), TaskChecks: plan.Patch[verification.CheckSpec]{Set: true, Value: []verification.CheckSpec{goCheck("a", "go", "test", "./...")}}})
	if !hasWholeSuite(res.Warnings["filtered"]) {
		t.Fatalf("update of checks: %v", res.Warnings)
	}
	// ...a filtered replacement does not...
	res = f.apply(plan.UpdateTask{Target: plan.Ref("wide"), TaskChecks: plan.Patch[verification.CheckSpec]{Set: true, Value: []verification.CheckSpec{goCheck("c", "go", "test", "./...", "-run", "TestY")}}})
	if len(res.Warnings["wide"]) != 0 {
		t.Fatalf("filtered update warned: %v", res.Warnings)
	}
	// ...a larger size silences it, and returning to small brings it back.
	large, small := "large", "small"
	res = f.apply(plan.UpdateTask{Target: plan.Ref("pkg"), Size: &large})
	if len(res.Warnings["pkg"]) != 0 {
		t.Fatalf("large size warned: %v", res.Warnings)
	}
	res = f.apply(plan.UpdateTask{Target: plan.Ref("pkg"), Size: &small})
	if !hasWholeSuite(res.Warnings["pkg"]) {
		t.Fatalf("update of size: %v", res.Warnings)
	}
}
