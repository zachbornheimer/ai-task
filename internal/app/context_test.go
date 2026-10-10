package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TestContextCarriesEverythingAnAgentAskedFor checks the working context
// against what the pilot agents said they were missing: the checks as the
// contract, the regression suite and target, the workspace and lease,
// the handoff with the last failure's output, files already changed on
// the branch, and the sibling tasks that are not theirs.
func TestContextCarriesEverythingAnAgentAskedFor(t *testing.T) {
	f := newGitFixture(t)
	a := f.addWith("store", "test -f store.go && grep -q 'func Open' store.go")
	b := f.add("other")
	f.apply(plan.AddGroup{Key: "g", Title: "Group"})
	s := f.claim(string(a))
	f.commit(s.Workspace, "store.go", "package store\n")
	if _, err := f.e.Log(f.ctx, s.Token, execution.LogEntry{Done: "skeleton", Next: "add Open(path)", Learned: "needs the sqlite driver"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete); fault.CodeOf(err) != fault.CodeVerificationFailed {
		t.Fatalf("expected a failed proof, got %v", err)
	}
	if _, err := f.e.Release(f.ctx, s.Token, app.ReleaseOptions{Note: "out of time"}); err != nil {
		t.Fatal(err)
	}
	s2 := f.claim(string(a))
	// Leave an uncommitted edit: the context lists the path, not Git's
	// status code.
	os.WriteFile(filepath.Join(s2.Workspace, "store.go"), []byte("package store // wip\n"), 0o644)
	// Worktrunk present: the prompt names its steps; absent: plain git.
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "wt"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cx, err := f.e.Context(f.ctx, f.proj.ID, "", s2.Token, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Task " + string(a),
		"`sh -c 'test -f store.go && grep -q '\\''func Open'\\'' store.go'`", // the check, shell-quoted
		"regress: `sh -c true`",
		"promotes it to main",        // target branch
		"Workspace: " + s2.Workspace, // where to work
		"Lease until",                // when to log
		"Next step recorded by the last attempt: add Open(path)",
		"learned: needs the sqlite driver",
		"Last `at verify complete`",
		"## Current state (attempt 2)",
		"unit-store failed",
		"Files this branch has changed: store.go",
		string(b) + " other", // sibling, not yours
		"Uncommitted changes in the workspace (review before editing): store.go\n",
		"Commit with `wt step commit`",
		"`wt step rebase main`",
	} {
		if !strings.Contains(cx.Prompt, want) {
			t.Fatalf("context lacks %q:\n%s", want, cx.Prompt)
		}
	}
	if strings.Index(cx.Prompt, "## Current state") > strings.Index(cx.Prompt, "Task checks") {
		t.Fatalf("a retry must read its state before the contract:\n%s", cx.Prompt)
	}
	if strings.Contains(cx.Prompt, "Agent execution contract") || cx.LeaseUntil == nil || len(cx.ChangedFiles) != 1 || len(cx.Siblings) != 1 || cx.Siblings[0].ID != b || !cx.Worktrunk {
		t.Fatalf("%+v", cx)
	}
	t.Setenv("PATH", gitOnlyPath(t))
	if plain, err := f.e.Context(f.ctx, f.proj.ID, "", s2.Token, false); err != nil || plain.Worktrunk || !strings.Contains(plain.Prompt, "Commit with git") || !strings.Contains(plain.Prompt, "`git merge main`") {
		t.Fatalf("without wt: %v %s", err, plain.Prompt)
	}
	// Groups are never siblings; a group is not a task.
	if _, err := f.e.Context(f.ctx, f.proj.ID, "g", "", false); fault.CodeOf(err) != fault.CodeInvalidInput {
		t.Fatalf("group context: %v", err)
	}
	// By reference, read-only, for a task nobody holds: no workspace, a
	// pointer to claim, and the rules on request.
	cb, err := f.e.Context(f.ctx, f.proj.ID, "other", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if cb.Workspace != "" || !strings.Contains(cb.Prompt, "at claim "+string(b)) || !strings.HasSuffix(strings.TrimSpace(cb.Prompt), "final verification.") || !strings.Contains(cb.Prompt, "## Agent execution contract") {
		t.Fatalf("%s", cb.Prompt)
	}
	if pc, err := f.e.Context(f.ctx, f.proj.ID, "", "", false); err != nil || pc.Task != nil || pc.Summary == nil {
		t.Fatalf("no ref, no token must describe the plan: %v %+v", err, pc)
	}
}

func TestProjectContextSaysWhatToDoNext(t *testing.T) {
	f := newGitFixture(t)
	empty, err := f.e.Context(f.ctx, f.proj.ID, "", "", false)
	if err != nil || empty.Task != nil || !strings.Contains(empty.Prompt, "Nothing is planned yet") || !strings.Contains(empty.Prompt, "at doctor") {
		t.Fatalf("%v %s", err, empty.Prompt)
	}
	a := f.add("a")
	ready, err := f.e.Context(f.ctx, f.proj.ID, "", "", true)
	if err != nil || len(ready.Claimable) != 1 || ready.Claimable[0].ID != a || !strings.Contains(ready.Prompt, "Claimable now") || !strings.Contains(ready.Prompt, "## Agent execution contract") {
		t.Fatalf("%v %s", err, ready.Prompt)
	}
	if _, err := f.e.Context(f.ctx, "", "", "", false); fault.CodeOf(err) != fault.CodeNoProject {
		t.Fatalf("no project: %v", err)
	}
}
