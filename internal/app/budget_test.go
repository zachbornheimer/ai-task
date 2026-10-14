package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/must"
	"github.com/zachbornheimer/ai-task/internal/plan"
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
	f.setProofBudget(300 * time.Millisecond)
	a := f.addWith("slow", "sleep 1")
	s := f.claim(string(a))
	f.commit(s.Workspace, "a.txt", "a")
	// Diagnostic and final runs both report the timeout class.
	r, err := f.e.Verify(f.ctx, s.Token, verification.ModeProof)
	if fault.CodeOf(err) != fault.CodeCheckFailed || len(r.Evidence) != 1 || r.Evidence[0].Outcome != verification.OutcomeTimeout || !strings.Contains(r.Evidence[0].Message, "small task-check budget") {
		t.Fatalf("diagnostic: %v %+v", err, r)
	}
	r, err = f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	if fault.CodeOf(err) != fault.CodeCheckFailed || !strings.Contains(r.Message, "built too slow") || !strings.Contains(r.Message, "-run") || !strings.Contains(r.Message, "--size") || !strings.Contains(r.Summary, "timed out: unit-slow") {
		t.Fatalf("complete: %v %+v", err, r)
	}
	v := f.show(string(a))
	if v.Failures != 0 || !strings.Contains(v.LastError, "CHECK_FAILED") {
		t.Fatalf("a timeout must not be a strike: failures=%d last_error=%q", v.Failures, v.LastError)
	}
	if !strings.Contains(v.LastError, "-run") || !strings.Contains(v.LastError, "--size") {
		t.Fatalf("timeout last_error must name -run and --size: %q", v.LastError)
	}
	// The planner declares a larger size: same check, now within budget,
	// and the warning says it used more than half of a small budget only
	// when that is the case.
	medium := "medium"
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(a), Size: &medium}}}); err != nil {
		t.Fatal(err)
	}
	if f.show(string(a)).Size != task.SizeMedium {
		t.Fatal("size not updated")
	}
	f.setProofBudget(5 * time.Second)
	res := f.complete(s)
	if !res.Completed || len(res.Warnings) != 0 {
		t.Fatalf("medium: %+v", res)
	}
	// A slow regression gate times out as the planner's problem.
	f.setRegression(check("regress", "sleep 1", true))
	f.setProofBudget(300 * time.Millisecond)
	b := f.add("b")
	sb := f.claim(string(b))
	f.commit(sb.Workspace, "b.txt", "b")
	r, err = f.e.Verify(f.ctx, sb.Token, verification.ModeComplete)
	if fault.CodeOf(err) != fault.CodeCheckFailed || !strings.Contains(r.Message, "regression budget") || !strings.Contains(r.Message, "atask update ID --size medium|large") {
		t.Fatalf("regression timeout: %v %+v", err, r)
	}
	if f.show(string(b)).Failures != 0 {
		t.Fatal("regression timeout charged a strike")
	}
	// Near the budget: a passing check that used over half of it warns.
	f.setRegression(check("regress", "test -d .", true))
	f.setProofBudget(1500 * time.Millisecond)
	cTask := f.addWith("near", "sleep 1")
	sc := f.claim(string(cTask))
	f.commit(sc.Workspace, "c.txt", "c")
	res, err = f.e.Verify(f.ctx, sc.Token, verification.ModeComplete)
	if err != nil || !res.Completed || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "unit-near") {
		t.Fatalf("near budget: %v %+v", err, res)
	}
}

// TestChangedFilesReachChecks: checks see ATASK_CHANGED_FILES listing the
// paths the candidate changed against the target, and ATASK_TARGET_BRANCH.
func TestChangedFilesReachChecks(t *testing.T) {
	f := newGitFixture(t)
	a := f.addWith("tia", `echo target=$ATASK_TARGET_BRANCH; cat "$ATASK_CHANGED_FILES"; test "$ATASK_TARGET_BRANCH" = main && grep -qx store/x.go "$ATASK_CHANGED_FILES" && grep -qx README.md "$ATASK_CHANGED_FILES" && ! grep -qx f.txt "$ATASK_CHANGED_FILES"`)
	s := f.claim(string(a))
	must.OK(t, os.MkdirAll(filepath.Join(s.Workspace, "store"), 0o750))
	f.commit(s.Workspace, "store/x.go", "package store\n")
	f.commit(s.Workspace, "README.md", "r")
	if r, err := f.e.Verify(f.ctx, s.Token, verification.ModeProof); err != nil || !r.Passed {
		t.Fatalf("diagnostic: %v %+v", err, r)
	}
	f.complete(s)
}

// TestSmallSizeRejectsGoTestWithoutRun: a small task whose check is go
// test without -run cannot be filed; -run or a larger size is required.
func TestSmallSizeRejectsGoTestWithoutRun(t *testing.T) {
	f := newGitFixture(t)
	_, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
		plan.AddTask{Key: "w", Title: "w", TaskChecks: []verification.CheckSpec{{ID: "all", Command: []string{"go", "test", "./..."}, Required: true}}},
	}})
	if fault.CodeOf(err) != fault.CodeInvalidInput || !strings.Contains(err.Error(), "-run") || !strings.Contains(err.Error(), "--size") {
		t.Fatalf("unbounded go test on small: %v", err)
	}
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
		plan.AddTask{Key: "l", Title: "l", Size: "large", TaskChecks: []verification.CheckSpec{{ID: "all", Command: []string{"go", "test", "./..."}, Required: true}}},
	}}); err != nil {
		t.Fatalf("large: %v", err)
	}
}
