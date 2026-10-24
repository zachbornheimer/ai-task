package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/process"
	"github.com/zachbornheimer/ai-task/internal/proof"
	"github.com/zachbornheimer/ai-task/internal/testgit"
)

// isolatedMergeHome is a HOME outside every write grant of the sandbox
// policy: the usual t.TempDir() lives under systemTempRoot
// (/private/var/folders), which the policy grants wholesale, so a home
// there cannot prove a denial. /private/tmp is writable and ungranted.
func isolatedMergeHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/private/tmp", "atask-merge-home-")
	if err != nil {
		t.Skipf("cannot create an isolated home under /private/tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", resolved)
	return resolved
}

// linkedWorktreeRepo builds the real atask layout under an isolated HOME:
// the main checkout at Developer/Personal/ai-task, a .worktrees root beside
// it, and a linked worktree of the feature branch as the completion
// workspace. With targetInWorktree the target branch main is checked out in
// a second worktree under .worktrees; otherwise it stays checked out in the
// main checkout, as it is in a live repository.
func linkedWorktreeRepo(t *testing.T, targetInWorktree bool) (repo, ws, worktrees, tip string) {
	t.Helper()
	home := isolatedMergeHome(t)
	repo = filepath.Join(home, "Developer", "Personal", "ai-task")
	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatal(err)
	}
	initBranch := "main"
	if targetInWorktree {
		initBranch = "holder" // main must not be checked out in the main checkout
	}
	gitIn(t, repo, "init", "-q", "-b", initBranch)
	writeMergeFile(t, filepath.Join(repo, "a.txt"), "a\n")
	gitIn(t, repo, "add", "a.txt")
	gitIn(t, repo, "commit", "-qm", "chore: base")
	if targetInWorktree {
		gitIn(t, repo, "branch", "main")
	}
	worktrees = filepath.Join(home, "Developer", "Personal", ".worktrees")
	if err := os.MkdirAll(worktrees, 0o750); err != nil {
		t.Fatal(err)
	}
	if targetInWorktree {
		gitIn(t, repo, "worktree", "add", "-q", filepath.Join(worktrees, "target"), "main")
	}
	ws = filepath.Join(worktrees, "at-merge")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "feature", ws)
	writeMergeFile(t, filepath.Join(ws, "b.txt"), "b\n")
	gitIn(t, ws, "add", "b.txt")
	gitIn(t, ws, "commit", "-qm", "feat: work")
	tip = gitOut(t, ws, "rev-parse", "HEAD")
	return repo, ws, worktrees, tip
}

// runMergeAction runs the on-complete wt merge the way runCompletionActions
// does: through the real runner and executeAction, confined by the real
// sandbox policy, with [sandbox].extra_write granting the .worktrees root.
func runMergeAction(t *testing.T, ws, extraWrite string) (proof.ActionStatus, proof.RunResult) {
	t.Helper()
	env := checkEnv(ws)
	cfg := proof.SandboxConfig{Mode: process.SandboxStrict, ExtraWrite: []string{extraWrite}}
	runner := proof.NewRunner(
		proof.WithSandbox(sandboxPolicy(cfg)),
		proof.WithEnv(env),
		proof.WithBudget(time.Minute),
	)
	p, err := proof.Parse("run: wt -y merge main --no-squash --no-remove")
	if err != nil {
		t.Fatal(err)
	}
	spec := proof.ActionSpec{Preset: p.Preset, Subject: p.Subject, Options: p.Options}
	return executeAction(context.Background(), runner, spec, ws)
}

// TestCompletionActionMergesFromLinkedWorktree is the atask dogfood
// landing: the workspace is a linked worktree, the target branch is checked
// out in the main checkout, and the on-complete action merges the worktree
// branch into it.
func TestCompletionActionMergesFromLinkedWorktree(t *testing.T) {
	if runtime.GOOS != "darwin" || process.Inherited() {
		t.Skip("needs a fresh Seatbelt sandbox on macOS")
	}
	for _, tool := range []string{"sandbox-exec", "wt"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	repo, ws, worktrees, tip := linkedWorktreeRepo(t, false)
	status, run := runMergeAction(t, ws, worktrees)
	if status != proof.ActionSucceeded {
		t.Fatalf("on-complete wt merge from linked-worktree workspace %s:\n%s", status, run.Output)
	}
	if got := gitOut(t, repo, "rev-parse", "main"); got != tip {
		t.Fatalf("main is %s, want the landed tip %s", got, tip)
	}
	if _, err := os.Stat(filepath.Join(repo, "b.txt")); err != nil {
		t.Fatalf("landed file missing from the main checkout: %v", err)
	}
}

// TestCompletionActionMergesWhenTargetIsGrantedWorktree proves the same
// landing when the target branch is checked out in a second linked worktree
// beneath the granted .worktrees root: no write leaves the existing grants.
func TestCompletionActionMergesWhenTargetIsGrantedWorktree(t *testing.T) {
	if runtime.GOOS != "darwin" || process.Inherited() {
		t.Skip("needs a fresh Seatbelt sandbox on macOS")
	}
	for _, tool := range []string{"sandbox-exec", "wt"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	_, ws, worktrees, tip := linkedWorktreeRepo(t, true)
	status, run := runMergeAction(t, ws, worktrees)
	if status != proof.ActionSucceeded {
		t.Fatalf("on-complete wt merge with a granted target worktree %s:\n%s", status, run.Output)
	}
	target := filepath.Join(worktrees, "target")
	if got := gitOut(t, target, "rev-parse", "main"); got != tip {
		t.Fatalf("main is %s, want the landed tip %s", got, tip)
	}
	if _, err := os.Stat(filepath.Join(target, "b.txt")); err != nil {
		t.Fatalf("landed file missing from the target worktree: %v", err)
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := testgit.Command(t, dir, args...).CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := testgit.Command(t, dir, args...).Output()
	if err != nil {
		t.Fatalf("git %s in %s: %v", strings.Join(args, " "), dir, err)
	}
	return strings.TrimSpace(string(out))
}

func writeMergeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
