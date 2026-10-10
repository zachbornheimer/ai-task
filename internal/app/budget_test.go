package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TestBudgetsTimeOutWithoutAStrike: a task check that outruns the task's
// size budget is a timeout, its own result code, never a strike; the
// message says it is the verification's problem; a larger size lets the
// same check pass; a slow regression gate is reported as the planner's
// problem; and a check near its budget earns a warning.
func TestBudgetsTimeOutWithoutAStrike(t *testing.T) {
	f := newGitFixture(t)
	f.proj.Budgets = project.Budgets{Small: 300 * time.Millisecond, Medium: 5 * time.Second, Regression: 5 * time.Second}
	if err := f.e.UpdateProject(f.ctx, f.proj); err != nil {
		t.Fatal(err)
	}
	a := f.addWith("slow", "sleep 1")
	s := f.claim(string(a))
	f.commit(s.Workspace, "a.txt", "a")
	// Diagnostic and final runs both report the timeout class.
	r, err := f.e.Verify(f.ctx, s.Token, verification.ModeTask)
	if fault.CodeOf(err) != fault.CodeVerificationTimeout || len(r.Evidence) != 1 || r.Evidence[0].Outcome != verification.OutcomeTimeout || !strings.Contains(r.Evidence[0].Message, "small task-check budget") {
		t.Fatalf("diagnostic: %v %+v", err, r)
	}
	r, err = f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	if fault.CodeOf(err) != fault.CodeVerificationTimeout || !strings.Contains(r.Message, "built too slow") || !strings.Contains(r.Summary, "timed out: unit-slow") {
		t.Fatalf("complete: %v %+v", err, r)
	}
	if v := f.show(string(a)); v.Failures != 0 || !strings.Contains(v.LastError, "VERIFICATION_TIMEOUT") {
		t.Fatalf("a timeout must not be a strike: failures=%d last_error=%q", v.Failures, v.LastError)
	}
	// The planner declares a larger size: same check, now within budget,
	// and the warning says it used more than half of a small budget only
	// when that is the case.
	medium := "medium"
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Planner: true, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(a), Size: &medium}}}); err != nil {
		t.Fatal(err)
	}
	if f.show(string(a)).Size != task.SizeMedium {
		t.Fatal("size not updated")
	}
	res := f.complete(s)
	if !res.Completed || len(res.Warnings) != 0 {
		t.Fatalf("medium: %+v", res)
	}
	// A slow regression gate times out as the planner's problem.
	f.setRegression(check("regress", "sleep 1", true))
	f.proj, _ = f.e.Project(f.ctx, f.proj.ID)
	f.proj.Budgets.Regression = 300 * time.Millisecond
	if err := f.e.UpdateProject(f.ctx, f.proj); err != nil {
		t.Fatal(err)
	}
	b := f.add("b")
	sb := f.claim(string(b))
	f.commit(sb.Workspace, "b.txt", "b")
	r, err = f.e.Verify(f.ctx, sb.Token, verification.ModeComplete)
	if fault.CodeOf(err) != fault.CodeVerificationTimeout || !strings.Contains(r.Message, "regression budget") || !strings.Contains(r.Message, "planner") {
		t.Fatalf("regression timeout: %v %+v", err, r)
	}
	if f.show(string(b)).Failures != 0 {
		t.Fatal("regression timeout charged a strike")
	}
	// Near the budget: a passing check that used over half of it warns.
	f.proj.Budgets.Regression = 10 * time.Second
	f.proj.Budgets.Small = 1500 * time.Millisecond
	if err := f.e.UpdateProject(f.ctx, f.proj); err != nil {
		t.Fatal(err)
	}
	cTask := f.addWith("near", "sleep 1")
	sc := f.claim(string(cTask))
	f.commit(sc.Workspace, "c.txt", "c")
	res, err = f.e.Verify(f.ctx, sc.Token, verification.ModeComplete)
	if err != nil || !res.Completed || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "unit-near") {
		t.Fatalf("near budget: %v %+v", err, res)
	}
}

// TestChangedFilesReachChecks: checks see AT_CHANGED_FILES listing the
// paths the candidate changed against the target, and AT_TARGET_BRANCH.
func TestChangedFilesReachChecks(t *testing.T) {
	f := newGitFixture(t)
	a := f.addWith("tia", `echo target=$AT_TARGET_BRANCH; cat "$AT_CHANGED_FILES"; test "$AT_TARGET_BRANCH" = main && grep -qx store/x.go "$AT_CHANGED_FILES" && grep -qx README.md "$AT_CHANGED_FILES" && ! grep -qx f.txt "$AT_CHANGED_FILES"`)
	s := f.claim(string(a))
	os.MkdirAll(filepath.Join(s.Workspace, "store"), 0o755)
	f.commit(s.Workspace, "store/x.go", "package store\n")
	f.commit(s.Workspace, "README.md", "r")
	if r, err := f.e.Verify(f.ctx, s.Token, verification.ModeTask); err != nil || !r.Passed {
		t.Fatalf("diagnostic: %v %+v", err, r)
	}
	f.complete(s)
}

// TestPlanWarnsOnWholeSuiteSmallTask: a small task whose check runs the
// whole module is warned about at planning time.
func TestPlanWarnsOnWholeSuiteSmallTask(t *testing.T) {
	f := newGitFixture(t)
	res := f.apply(plan.AddTask{Key: "w", Title: "w", TaskChecks: []verification.CheckSpec{{ID: "all", Command: []string{"go", "test", "./..."}, Required: true}}})
	if w := res.Warnings["w"]; len(w) == 0 || !strings.Contains(w[0], "whole-suite") {
		t.Fatalf("warnings: %v", res.Warnings)
	}
	res = f.apply(plan.AddTask{Key: "l", Title: "l", Size: "large", TaskChecks: []verification.CheckSpec{{ID: "all", Command: []string{"go", "test", "./..."}, Required: true}}})
	if len(res.Warnings["l"]) != 0 {
		t.Fatalf("large must not warn: %v", res.Warnings)
	}
	var _ = app.DefaultGuidelines
}
