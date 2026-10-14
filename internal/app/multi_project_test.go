package app_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
)

type multiProjectBoard struct {
	t   *testing.T
	ctx context.Context
	e   *app.Engine
}

func newMultiProjectBoard(t *testing.T) *multiProjectBoard {
	t.Helper()
	ctx := context.Background()
	e, err := app.Open(ctx, app.Config{Path: filepath.Join(t.TempDir(), "at.db"), Actor: "zach", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return &multiProjectBoard{t: t, ctx: ctx, e: e}
}

func (b *multiProjectBoard) project(name string) project.ID {
	b.t.Helper()
	p, err := b.e.AddProject(b.ctx, name)
	if err != nil {
		b.t.Fatalf("add project %s: %v", name, err)
	}
	return p.ID
}

func (b *multiProjectBoard) add(title string, home project.ID, alsoIn ...project.ID) (task.ID, error) {
	b.t.Helper()
	res, err := b.e.Apply(b.ctx, plan.ChangeSet{ProjectID: home, Operations: []plan.Change{plan.AddTask{
		Title: title, AlsoIn: alsoIn,
		Proof:      mustParseProof(b.t, "run: go version"),
		Regression: mustParseProof(b.t, "run: go version"),
	}}})
	if err != nil {
		return "", err
	}
	return res.Created["#0"], nil
}

func (b *multiProjectBoard) show(id task.ID) app.TaskView {
	b.t.Helper()
	v, err := b.e.Show(b.ctx, "", string(id), false)
	if err != nil {
		b.t.Fatal(err)
	}
	return v
}

func (b *multiProjectBoard) listed(pid project.ID) []task.ID {
	b.t.Helper()
	snap, err := b.e.List(b.ctx, app.ListQuery{ProjectID: pid})
	if err != nil {
		b.t.Fatal(err)
	}
	var ids []task.ID
	for i := range snap.Tasks {
		ids = append(ids, snap.Tasks[i].ID)
	}
	return ids
}

func (b *multiProjectBoard) setProjects(id task.ID, first project.ID, all ...project.ID) error {
	b.t.Helper()
	_, err := b.e.Apply(b.ctx, plan.ChangeSet{ProjectID: first, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(id), Projects: plan.Replace(all)}}})
	return err
}

func (b *multiProjectBoard) require(id, needs task.ID, via project.ID) error {
	b.t.Helper()
	_, err := b.e.Apply(b.ctx, plan.ChangeSet{ProjectID: via, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(id), AddRequires: []plan.Ref{plan.Ref(needs)}}}})
	return err
}

func TestTaskBelongsToMultipleProjects(t *testing.T) {
	b := newMultiProjectBoard(t)
	a, bb, c := b.project("A"), b.project("B"), b.project("C")

	id, err := b.add("shared work", a, bb)
	if err != nil {
		t.Fatal(err)
	}
	v := b.show(id)
	if v.Project != "A" || !slices.Equal(v.Projects, []string{"A", "B"}) {
		t.Fatalf("home/projects = %q %v, want A [A B]", v.Project, v.Projects)
	}
	for _, pid := range []project.ID{a, bb} {
		if !slices.Contains(b.listed(pid), id) {
			t.Errorf("list --project %s must include the task", pid)
		}
	}
	if slices.Contains(b.listed(c), id) {
		t.Error("a project the task does not name must not list it")
	}
	snap, _ := b.e.List(b.ctx, app.ListQuery{ProjectID: bb})
	if got := snap.Tasks[0].Project; got != "A" {
		t.Errorf("list from membership B shows project %q, want home A", got)
	}

	// Before the first claim, reordering moves the home.
	if err := b.setProjects(id, bb, bb, a); err != nil {
		t.Fatalf("reorder before claim: %v", err)
	}
	if v := b.show(id); v.Project != "B" || !slices.Equal(v.Projects, []string{"B", "A"}) {
		t.Fatalf("after reorder: %q %v", v.Project, v.Projects)
	}

	// After the first claim, the home is fixed.
	if _, err := b.e.Claim(b.ctx, app.ClaimRequest{ProjectID: bb, TaskID: &id}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	err = b.setProjects(id, a, a, bb)
	if fault.CodeOf(err) != fault.CodeInvalidInput {
		t.Fatalf("home change after claim: want INVALID_INPUT, got %v", err)
	}
	if err := b.setProjects(id, bb, bb, a, c); err != nil {
		t.Fatalf("membership edit keeping the home after claim: %v", err)
	}
}

func TestRequiresNeedsASharedProject(t *testing.T) {
	b := newMultiProjectBoard(t)
	a, bb, c := b.project("A"), b.project("B"), b.project("C")
	x, _ := b.add("x", a, bb)
	y, _ := b.add("y", bb)
	z, _ := b.add("z", c)
	if err := b.require(x, y, a); err != nil {
		t.Fatalf("requires across a shared project: %v", err)
	}
	if err := b.require(x, z, a); fault.CodeOf(err) != fault.CodeInvalidInput {
		t.Fatalf("requires across disjoint projects: want INVALID_INPUT, got %v", err)
	}
}
