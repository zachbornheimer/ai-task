package app_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/must"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/testgit"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// repoSwitcher creates the worktree in the repo Ensure is asked for, so a
// test can claim tasks whose homes are different repositories.
type repoSwitcher struct {
	t    testing.TB
	root string
}

func (a repoSwitcher) Ensure(ctx context.Context, repo, branch, base string, create bool) (string, error) {
	if err := os.MkdirAll(a.root, 0o750); err != nil {
		return "", err
	}
	root := a.root
	if resolved, err := filepath.EvalSymlinks(a.root); err == nil {
		root = resolved
	}
	path := filepath.Join(root, strings.ReplaceAll(branch, "/", "-"))
	if create {
		cmd := testgit.CommandContext(ctx, a.t, repo, "branch", branch, base)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", &switchError{what: "branch", err: err, out: strings.TrimSpace(string(out))}
		}
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return path, nil
	}
	cmd := testgit.CommandContext(ctx, a.t, repo, "worktree", "add", path, branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", &switchError{what: "worktree add", err: err, out: strings.TrimSpace(string(out))}
	}
	return path, nil
}

type switchError struct {
	what string
	err  error
	out  string
}

func (e *switchError) Error() string {
	return e.what + ": " + e.err.Error() + "\n" + e.out
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	must.OK(t, os.MkdirAll(dir, 0o750))
	testgit.Run(t, dir, "init", "-q", "-b", "main")
	testgit.Run(t, dir, "config", "user.name", "t")
	testgit.Run(t, dir, "config", "user.email", "t@t")
	must.OK(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one"), 0o600))
	testgit.Run(t, dir, "add", "f.txt")
	testgit.Run(t, dir, "commit", "-q", "-m", "one")
}

func TestClaimSkipsRequirementFromAnotherRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	root := t.TempDir()
	isa := filepath.Join(root, "isa")
	eapp := filepath.Join(root, "eapp")
	initRepo(t, isa)
	initRepo(t, eapp)

	e, err := app.Open(ctx, app.Config{
		Path:   filepath.Join(root, "at.db"),
		Actor:  "t",
		Cwd:    root,
		Switch: repoSwitcher{t: t, root: filepath.Join(root, "ws")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { must.OK(t, e.Close()) })

	isaProj, err := e.InitProject(ctx, "isa", isa)
	if err != nil {
		t.Fatal(err)
	}
	eappProj, err := e.InitProject(ctx, "eapp", eapp)
	if err != nil {
		t.Fatal(err)
	}
	proofSpec := mustParseProof(t, "run: go version")
	regression := mustParseProof(t, "run: go version")
	up, err := e.Apply(ctx, plan.ChangeSet{ProjectID: eappProj.ID, Operations: []plan.Change{plan.AddTask{
		Key: "upstream", Title: "upstream", Proof: proofSpec, Regression: regression,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	upstream := up.Created["upstream"]
	down, err := e.Apply(ctx, plan.ChangeSet{ProjectID: isaProj.ID, Operations: []plan.Change{plan.AddTask{
		Key: "downstream", Title: "downstream", AlsoIn: []project.ID{eappProj.ID}, Requires: []plan.Ref{plan.Ref(upstream)}, Proof: proofSpec, Regression: regression,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	downstream := down.Created["downstream"]

	claimed, err := e.Claim(ctx, app.ClaimRequest{TaskID: &upstream})
	if err != nil {
		t.Fatal(err)
	}
	must.OK(t, os.WriteFile(filepath.Join(claimed.Workspace, "outbox.txt"), []byte("from eapp"), 0o600))
	testgit.Run(t, claimed.Workspace, "add", "outbox.txt")
	testgit.Run(t, claimed.Workspace, "commit", "-q", "-m", "outbox")
	res, err := e.Verify(ctx, claimed.Token, verification.ModeComplete)
	if err != nil || !res.Completed {
		t.Fatalf("complete upstream: %+v %v", res, err)
	}

	got, err := e.Claim(ctx, app.ClaimRequest{TaskID: &downstream})
	if err != nil {
		t.Fatalf("claim downstream: %v", err)
	}
	if _, err := os.Stat(filepath.Join(got.Workspace, "outbox.txt")); err == nil {
		t.Fatal("downstream worktree contains the other repository's file")
	}
	view, err := e.Show(ctx, isaProj.ID, string(downstream), false)
	if err != nil {
		t.Fatal(err)
	}
	var notes strings.Builder
	if view.Handoff != nil {
		for _, entry := range view.Handoff.RecentLogs {
			notes.WriteString(entry.Note)
			notes.WriteByte('\n')
		}
	}
	if !strings.Contains(notes.String(), "completed in another repository") {
		t.Fatalf("claim log lacks the foreign-revision note: %q", notes.String())
	}
	prompt, err := e.Context(ctx, isaProj.ID, string(downstream), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt.Prompt, "on the target branch") {
		t.Fatal("context says the other repository's work is on the target branch")
	}
	if !strings.Contains(prompt.Prompt, "completed in another repository") {
		t.Fatalf("context lacks the foreign-revision note: %s", prompt.Prompt)
	}
}
