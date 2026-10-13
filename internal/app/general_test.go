package app_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/task"
)

func openBoard(t *testing.T) (*app.Engine, context.Context) {
	t.Helper()
	ctx := context.Background()
	e, err := app.Open(ctx, app.Config{
		Path:  filepath.Join(t.TempDir(), "at.db"),
		Actor: "zach",
		Cwd:   t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e, ctx
}

func TestResolveProjectFallsBackToGeneral(t *testing.T) {
	e, ctx := openBoard(t)
	p, err := e.ResolveProject(ctx, "", t.TempDir())
	if err != nil {
		t.Fatalf("unregistered dir: %v", err)
	}
	if !p.IsGeneral() {
		t.Fatalf("want general, got %+v", p)
	}
	again, err := e.ResolveProject(ctx, "", t.TempDir())
	if err != nil || again.ID != p.ID {
		t.Fatalf("general must be stable: %v %+v", err, again)
	}
}

func TestResolveProjectKeepsRegisteredRepo(t *testing.T) {
	f := newGitFixture(t)
	p, err := f.e.ResolveProject(f.ctx, "", f.repo)
	if err != nil || p.ID != f.proj.ID {
		t.Fatalf("registered repo: %v %+v want %s", err, p, f.proj.ID)
	}
}

func TestAddSameOpenTitleReusesTask(t *testing.T) {
	f := newGitFixture(t)
	op := plan.AddTask{Title: "rename host loops", TaskChecks: okChecks()}
	first, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{op}})
	if err != nil || first.Changed != 1 {
		t.Fatalf("first add: %+v %v", first, err)
	}
	id := first.Created["#0"]
	second, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{op}})
	if err != nil {
		t.Fatalf("second add: %v", err)
	}
	if second.Changed != 0 {
		t.Fatalf("second add must be a no-op, changed=%d", second.Changed)
	}
	if second.Created["#0"] != id {
		t.Fatalf("second add must return %s, got %s", id, second.Created["#0"])
	}
	if n := count(f, app.FilterAll); n != 1 {
		t.Fatalf("got %d tasks, want 1", n)
	}
}

func TestGeneralAddRequiresCheck(t *testing.T) {
	e, ctx := openBoard(t)
	p, err := e.ResolveProject(ctx, "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Apply(ctx, plan.ChangeSet{ProjectID: p.ID, Operations: []plan.Change{plan.AddTask{Title: "file this from ~/.agents"}}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := e.Show(ctx, p.ID, string(res.Created["#0"]), false)
	if err != nil || v.Status != task.StatusDraft {
		t.Fatalf("a task without proofs is a draft: %v %v", v.Status, err)
	}
}

func TestGeneralAddStampsOrigin(t *testing.T) {
	e, ctx := openBoard(t)
	p, err := e.ResolveProject(ctx, "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Apply(ctx, plan.ChangeSet{ProjectID: p.ID, Operations: []plan.Change{plan.AddTask{Title: "note with a gate", TaskChecks: okChecks()}}})
	if err != nil || res.Changed != 1 {
		t.Fatalf("add: %+v %v", res, err)
	}
	v, err := e.Show(ctx, p.ID, string(res.Created["#0"]), false)
	if err != nil {
		t.Fatal(err)
	}
	if v.Creator != "zach" || v.CreatedCwd == "" {
		t.Fatalf("origin: creator=%q cwd=%q", v.Creator, v.CreatedCwd)
	}
}

func TestCodeProjectAddWithoutCheckStillFails(t *testing.T) {
	f := newGitFixture(t)
	res, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.AddTask{Title: "needs a gate"}}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.e.Show(f.ctx, f.proj.ID, string(res.Created["#0"]), false)
	if err != nil || v.Status != task.StatusDraft {
		t.Fatalf("a task without proofs is a draft: %v %v", v.Status, err)
	}
}

func TestGlobalListIncludesGeneralAndNamed(t *testing.T) {
	f := newGitFixture(t)
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.AddTask{Title: "from git", TaskChecks: okChecks()}}}); err != nil {
		t.Fatal(err)
	}
	u, err := f.e.EnsureGeneral(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: u.ID, Operations: []plan.Change{plan.AddTask{Title: "from anywhere", TaskChecks: okChecks()}}}); err != nil {
		t.Fatal(err)
	}
	snap, err := f.e.List(f.ctx, app.ListQuery{Filter: app.FilterOpen})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, v := range snap.Tasks {
		got[v.Title] = true
	}
	if !got["from git"] || !got["from anywhere"] {
		t.Fatalf("global list: %+v", got)
	}
	sum, err := f.e.Summary(f.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Open < 2 {
		t.Fatalf("board-wide summary should count both projects: %+v", sum)
	}
}
