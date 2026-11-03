package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/must"
	"github.com/zachbornheimer/ai-task/internal/testgit"
)

// newCopyFixture builds a git repo with ignored dependency content:
// node_modules/ with a nested file, an ignored top-level artifact, and
// a .worktrees/ directory holding worktree state.
func newCopyFixture(t *testing.T) (src, dst string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	src = filepath.Join(base, "src")
	dst = filepath.Join(base, "dst")
	must.OK(t, os.MkdirAll(src, 0o750))
	must.OK(t, os.MkdirAll(dst, 0o750))
	g := func(args ...string) string { return testgit.Run(t, src, args...) }
	g("init", "-q", "-b", "main")
	must.OK(t, os.WriteFile(filepath.Join(src, ".gitignore"), []byte("node_modules/\ndist/\n.worktrees/\n"), 0o644))
	must.OK(t, os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n"), 0o644))
	// The ignored dependency tree, including a built native addon.
	must.OK(t, os.MkdirAll(filepath.Join(src, "node_modules", "pi_natives", "build"), 0o750))
	must.OK(t, os.WriteFile(filepath.Join(src, "node_modules", "pi_natives", "build", "Release.node"), []byte("native"), 0o644))
	must.OK(t, os.WriteFile(filepath.Join(src, "node_modules", "pi_natives", "index.js"), []byte("module.exports = 1\n"), 0o644))
	// A nested ignored artifact outside node_modules.
	must.OK(t, os.MkdirAll(filepath.Join(src, "dist", "sub"), 0o750))
	must.OK(t, os.WriteFile(filepath.Join(src, "dist", "sub", "out.bin"), []byte("artifact"), 0o644))
	// Worktree state that must never be copied.
	must.OK(t, os.MkdirAll(filepath.Join(src, ".worktrees", "other"), 0o750))
	must.OK(t, os.WriteFile(filepath.Join(src, ".worktrees", "other", "HEAD"), []byte("state"), 0o644))
	g("add", ".")
	g("commit", "-q", "-m", "init")
	// A git worktree at dst so copied state stays out of VCS.
	testgit.Run(t, src, "worktree", "add", "--detach", dst, "HEAD")
	must.OK(t, os.RemoveAll(filepath.Join(dst, "node_modules")))
	return src, dst
}

func TestCopyIgnoredProvisionsDependencies(t *testing.T) {
	src, dst := newCopyFixture(t)
	if err := CopyIgnored(context.Background(), src, dst); err != nil {
		t.Fatalf("CopyIgnored: %v", err)
	}
	for _, p := range []string{
		filepath.Join("node_modules", "pi_natives", "index.js"),
		filepath.Join("node_modules", "pi_natives", "build", "Release.node"),
		filepath.Join("dist", "sub", "out.bin"),
	} {
		if _, err := os.Stat(filepath.Join(dst, p)); err != nil {
			t.Errorf("%s was not copied: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, ".worktrees")); err == nil {
		t.Errorf(".worktrees worktree state was copied into the destination")
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); err != nil {
		t.Errorf("destination .git missing after copy: %v", err)
	}
}

func TestCopyIgnoredIsIdempotentAndSkipsExisting(t *testing.T) {
	src, dst := newCopyFixture(t)
	if err := CopyIgnored(context.Background(), src, dst); err != nil {
		t.Fatalf("first CopyIgnored: %v", err)
	}
	marker := filepath.Join(dst, "node_modules", "pi_natives", "index.js")
	must.OK(t, os.WriteFile(marker, []byte("edited in destination"), 0o644))
	if err := CopyIgnored(context.Background(), src, dst); err != nil {
		t.Fatalf("second CopyIgnored: %v", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(got) != "edited in destination" {
		t.Errorf("second copy overwrote destination file: %q", got)
	}
}

func TestCopyIgnoredNoIgnoresIsNoOp(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	dst := filepath.Join(base, "dst")
	must.OK(t, os.MkdirAll(src, 0o750))
	g := func(args ...string) string { return testgit.Run(t, src, args...) }
	g("init", "-q", "-b", "main")
	must.OK(t, os.WriteFile(filepath.Join(src, "tracked.txt"), []byte("x"), 0o644))
	g("add", ".")
	g("commit", "-q", "-m", "init")
	testgit.Run(t, src, "worktree", "add", "--detach", dst, "HEAD")
	if err := CopyIgnored(context.Background(), src, dst); err != nil {
		t.Fatalf("CopyIgnored on repo without ignores: %v", err)
	}
	entries, err := os.ReadDir(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	var names []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".git") {
			names = append(names, e.Name())
		}
	}
	if len(names) != 1 || names[0] != "tracked.txt" {
		t.Errorf("copy ran on a repo with no ignored files; dst has %v", names)
	}
}
