package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	run("add", "a.txt")
	run("commit", "-q", "-m", "one")
	return dir
}

func TestInspectAndRequireClean(t *testing.T) {
	dir := initRepo(t)
	ctx := context.Background()
	st, err := RequireClean(ctx, dir)
	if err != nil || len(st.Revision) != 40 {
		t.Fatalf("%+v %v", st, err)
	}
	if !IsGitRepo(filepath.Join(dir, "sub", "dir")) || IsGitRepo(t.TempDir()) {
		t.Fatal("IsGitRepo")
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed"), 0o644)
	_, err = RequireClean(ctx, dir)
	if !fault.Is(err, fault.CodeWorkspaceDirty) {
		t.Fatalf("tracked change: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644)
	_, err = RequireClean(ctx, dir)
	if !fault.Is(err, fault.CodeWorkspaceDirty) {
		t.Fatalf("untracked file must count as dirty: %v", err)
	}
	os.Remove(filepath.Join(dir, "new.txt"))
	if _, err := RequireClean(ctx, dir); err != nil {
		t.Fatal(err)
	}
	_, err = Inspect(ctx, t.TempDir())
	if !fault.Is(err, fault.CodeWorkspaceUnavailable) {
		t.Fatalf("non-repo: %v", err)
	}
}
