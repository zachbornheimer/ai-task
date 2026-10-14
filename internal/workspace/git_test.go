package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/must"
	"github.com/zachbornheimer/ai-task/internal/testgit"
)

func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) { testgit.Run(t, dir, args...) }
	run("init", "-q")
	run("config", "user.name", "t")
	run("config", "user.email", "t@t")
	must.OK(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o600))
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
	must.OK(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed"), 0o600))
	_, err = RequireClean(ctx, dir)
	if !fault.Is(err, fault.CodeNotReady) {
		t.Fatalf("tracked change: %v", err)
	}
	must.OK(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o600))
	must.OK(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o600))
	_, err = RequireClean(ctx, dir)
	if !fault.Is(err, fault.CodeNotReady) {
		t.Fatalf("untracked file must count as dirty: %v", err)
	}
	must.OK(t, os.Remove(filepath.Join(dir, "new.txt")))
	if _, err := RequireClean(ctx, dir); err != nil {
		t.Fatal(err)
	}
	_, err = Inspect(ctx, t.TempDir())
	if !fault.Is(err, fault.CodeNotReady) {
		t.Fatalf("non-repo: %v", err)
	}
}
