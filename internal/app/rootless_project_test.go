package app_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func TestRootlessProjectLifecycle(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	e, err := app.Open(ctx, app.Config{Path: filepath.Join(t.TempDir(), "at.db"), Actor: "zach", Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })

	p, err := e.AddProject(ctx, "AI Tooling")
	if err != nil {
		t.Fatalf("add project: %v", err)
	}
	if p.RootPath != "" || p.TargetBranch != "" || !p.Rootless() {
		t.Fatalf("want rootless project, got %+v", p)
	}
	again, err := e.AddProject(ctx, "AI Tooling")
	if err != nil || again.ID != p.ID {
		t.Fatalf("second add must return %s: %v %+v", p.ID, err, again)
	}

	res, err := e.Apply(ctx, plan.ChangeSet{ProjectID: p.ID, Operations: []plan.Change{plan.AddTask{
		Title:      "write the notes",
		Proof:      mustParseProof(t, "run: go version"),
		Regression: mustParseProof(t, "run: go version"),
	}}})
	if err != nil || res.Changed != 1 {
		t.Fatalf("add task: %+v %v", res, err)
	}
	id := res.Created["#0"]
	s, err := e.Claim(ctx, app.ClaimRequest{ProjectID: p.ID, TaskID: &id})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if s.Workspace != cwd {
		t.Fatalf("workspace %q, want the creator's cwd %q", s.Workspace, cwd)
	}
	v, err := e.Verify(ctx, s.Token, verification.ModeComplete)
	if err != nil || !v.Passed {
		t.Fatalf("verify complete: %+v %v", v, err)
	}

	p.TargetBranch = "main"
	if err := e.UpdateProject(ctx, p); fault.CodeOf(err) != fault.CodeInvalidInput {
		t.Fatalf("target branch on a rootless project: want INVALID_INPUT, got %v", err)
	}
}
