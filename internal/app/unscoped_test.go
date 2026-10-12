package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/verification"
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

func TestResolveProjectFallsBackToUnscoped(t *testing.T) {
	e, ctx := openBoard(t)
	p, err := e.ResolveProject(ctx, "", t.TempDir())
	if err != nil {
		t.Fatalf("unregistered dir: %v", err)
	}
	if !p.Unscoped() {
		t.Fatalf("want unscoped, got %+v", p)
	}
	again, err := e.ResolveProject(ctx, "", t.TempDir())
	if err != nil || again.ID != p.ID {
		t.Fatalf("unscoped must be stable: %v %+v", err, again)
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

func TestUnscopedAddRequiresCheck(t *testing.T) {
	e, ctx := openBoard(t)
	p, err := e.ResolveProject(ctx, "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Apply(ctx, plan.ChangeSet{ProjectID: p.ID, Operations: []plan.Change{plan.AddTask{Title: "file this from ~/.agents"}}})
	wantCode(t, err, fault.CodeMissingVerification)
}

func TestUnscopedAddStampsOrigin(t *testing.T) {
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
	_, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.AddTask{Title: "needs a gate"}}})
	wantCode(t, err, fault.CodeMissingVerification)
}

func TestGlobalListIncludesUnscopedAndNamed(t *testing.T) {
	f := newGitFixture(t)
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.AddTask{Title: "from git", TaskChecks: okChecks()}}}); err != nil {
		t.Fatal(err)
	}
	u, err := f.e.EnsureUnscoped(f.ctx)
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
	_ = project.UnscopedID
}

func TestUnscopedWaitingClaimReturnsDoneOnEmptyBoard(t *testing.T) {
	e, ctx := openBoard(t)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := e.Claim(ctx, app.ClaimRequest{Wait: true})
	if !errors.Is(err, app.ErrDone) {
		t.Fatalf("empty unscoped wait: %v", err)
	}
}

func TestUnscopedWaitingClaimReturnsDoneAfterComplete(t *testing.T) {
	e, ctx := openBoard(t)
	p, err := e.EnsureUnscoped(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Apply(ctx, plan.ChangeSet{ProjectID: p.ID, Operations: []plan.Change{plan.AddTask{Title: "unscoped wait", TaskChecks: okChecks()}}})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Created["#0"]
	s, err := e.Claim(ctx, app.ClaimRequest{TaskID: &id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Verify(ctx, s.Token, verification.ModeComplete); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err = e.Claim(ctx, app.ClaimRequest{Wait: true})
	if !errors.Is(err, app.ErrDone) {
		t.Fatalf("completed unscoped wait: %v", err)
	}
}

func TestUnscopedContextOmitsGitInstructions(t *testing.T) {
	e, ctx := openBoard(t)
	p, err := e.EnsureUnscoped(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Apply(ctx, plan.ChangeSet{ProjectID: p.ID, Operations: []plan.Change{plan.AddTask{Title: "research note", TaskChecks: okChecks()}}})
	if err != nil {
		t.Fatal(err)
	}
	cx, err := e.Context(ctx, p.ID, string(res.Created["#0"]), "", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"Commit with", "promotes it", "wt step", "git merge"} {
		if strings.Contains(cx.Prompt, banned) {
			t.Fatalf("unscoped handoff must not include %q:\n%s", banned, cx.Prompt)
		}
	}
	if !strings.Contains(cx.Prompt, "live working directory") {
		t.Fatalf("unscoped handoff should describe live checks:\n%s", cx.Prompt)
	}
}
