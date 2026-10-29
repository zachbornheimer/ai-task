package app_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/must"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/proof"
	"github.com/zachbornheimer/ai-task/internal/testgit"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// repoWithCommit creates a Git repository on main with one commit.
func repoWithCommit(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	must.OK(t, os.MkdirAll(repo, 0o750))
	testgit.Run(t, repo, "init", "-q", "-b", "main")
	testgit.Run(t, repo, "config", "user.name", "t")
	testgit.Run(t, repo, "config", "user.email", "t@t")
	must.OK(t, os.WriteFile(filepath.Join(repo, "README"), []byte("one\n"), 0o600))
	testgit.Run(t, repo, "add", "README")
	testgit.Run(t, repo, "commit", "-q", "-m", "one")
	return repo
}

func repoTask(key string, requires ...plan.Ref) plan.AddTask {
	return plan.AddTask{
		Key: key, Title: key, Requires: requires,
		TaskChecks: []verification.CheckSpec{{ID: "unit-" + key, Command: []string{"sh", "-c", "test -d ."}, Required: true}},
		Regression: &proof.ProofSpec{Preset: "run", Subject: "test -d ."},
	}
}

// TestTaskRepoBinding: a task filed against a repository that nothing has
// registered claims with its own worktree on atask/<id>. The project is a
// plain planning scope; the repository travels on the task.
func TestTaskRepoBinding(t *testing.T) {
	repo := repoWithCommit(t)
	ctx := context.Background()
	e, err := app.Open(ctx, app.Config{
		Path:   filepath.Join(t.TempDir(), "at.db"),
		Cwd:    t.TempDir(), // not a repository: only the change set names one
		Switch: addSwitcher{t: t, repo: repo, root: filepath.Join(t.TempDir(), "ws")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { must.OK(t, e.Close()) })

	p, err := e.AddProject(ctx, "board")
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Apply(ctx, plan.ChangeSet{ProjectID: p.ID, Repo: repo, Operations: []plan.Change{repoTask("only")}})
	if err != nil {
		t.Fatalf("file a task against an unregistered repository: %v", err)
	}
	id := res.Created["only"]

	s, err := e.Claim(ctx, app.ClaimRequest{ProjectID: p.ID, TaskID: &id})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if s.Workspace == "" {
		t.Fatalf("want a worktree for %s, got none", id)
	}
	if want := "atask/" + string(id); s.Branch != want {
		t.Fatalf("branch %q, want %q", s.Branch, want)
	}
	if s.BaseRef != "main" {
		t.Fatalf("base %q, want main", s.BaseRef)
	}
	if _, err := os.Stat(filepath.Join(s.Workspace, "README")); err != nil {
		t.Fatalf("worktree is not a checkout of the repository: %v", err)
	}
}

// TestCrossRepoRequires: a requirement may cross repositories. The edge
// orders the work; the dependent's base is its own repository's branch, and
// the requirement's revision is not merged into it.
func TestCrossRepoRequires(t *testing.T) {
	repoA := repoWithCommit(t)
	repoB := repoWithCommit(t)
	ctx := context.Background()
	e, err := app.Open(ctx, app.Config{
		Path:        filepath.Join(t.TempDir(), "at.db"),
		Cwd:         repoA,
		Switch:      addSwitcher{t: t, repo: repoA, root: filepath.Join(t.TempDir(), "ws")},
		ProofBudget: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { must.OK(t, e.Close()) })

	p, err := e.AddProject(ctx, "board")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, plan.ChangeSet{ProjectID: p.ID, Repo: repoA, Operations: []plan.Change{repoTask("emit")}}); err != nil {
		t.Fatalf("file the producer: %v", err)
	}
	res, err := e.Apply(ctx, plan.ChangeSet{ProjectID: p.ID, Repo: repoB, Operations: []plan.Change{repoTask("consume", "emit")}})
	if err != nil {
		t.Fatalf("a requirement across repositories must be legal: %v", err)
	}
	consume := res.Created["consume"]

	producer, err := e.Show(ctx, p.ID, "emit", false)
	if err != nil {
		t.Fatal(err)
	}
	s, err := e.Claim(ctx, app.ClaimRequest{ProjectID: p.ID, TaskID: &producer.ID})
	if err != nil {
		t.Fatalf("claim producer: %v", err)
	}
	must.OK(t, os.WriteFile(filepath.Join(s.Workspace, "produced.txt"), []byte("from A\n"), 0o600))
	testgit.Run(t, s.Workspace, "add", "produced.txt")
	testgit.Run(t, s.Workspace, "commit", "-q", "-m", "produced")
	if v, err := e.Verify(ctx, s.Token, verification.ModeComplete); err != nil || !v.Completed {
		t.Fatalf("complete the producer: %+v %v", v, err)
	}

	c, err := e.Claim(ctx, app.ClaimRequest{ProjectID: p.ID, TaskID: &consume})
	if err != nil {
		t.Fatalf("claim the consumer: %v", err)
	}
	if c.BaseRef != "main" {
		t.Fatalf("consumer base %q, want the repository's own main", c.BaseRef)
	}
	if _, err := os.Stat(filepath.Join(c.Workspace, "produced.txt")); !os.IsNotExist(err) {
		t.Fatalf("the producer's revision must not be merged into another repository's base: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.Workspace, "README")); err != nil {
		t.Fatalf("consumer worktree is not repo B's checkout: %v", err)
	}
}
