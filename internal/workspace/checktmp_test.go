package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckTmpNameDoesNotGrowWithDir(t *testing.T) {
	t.Setenv(CheckTmpRootEnv, "")
	short := CheckTmp("/w")
	long := CheckTmp("/" + strings.Repeat("deep/", 60) + "worktree")
	if len(short) != len(long) {
		t.Errorf("TMPDIR length depends on the dir: %q vs %q", short, long)
	}
	if short == long {
		t.Errorf("different workspaces have the same TMPDIR: %q", short)
	}
	if repeated := CheckTmp("/w"); repeated != short {
		t.Errorf("TMPDIR is unstable: got %q, want %q", repeated, short)
	}
}

func TestEnsureCheckTmpRefusesSharedRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	// Set permissions through the open fixture, independent of umask and path replacement.
	dir, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dir.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := dir.Chmod(".", 0o750); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("shared root mode: got %04o, want 0750", info.Mode().Perm())
	}
	t.Setenv(CheckTmpRootEnv, root)
	if tmp, err := EnsureCheckTmp("/w"); err == nil {
		t.Fatalf("EnsureCheckTmp accepted shared root %s and returned %s", root, tmp)
	}
}

func TestCheckTmpDirectoryLifecycle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	t.Setenv(CheckTmpRootEnv, root)
	tmp, err := EnsureCheckTmp("/w")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("EnsureCheckTmp returned %s, not a directory", tmp)
	}
	RemoveCheckTmp("/w")
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("RemoveCheckTmp left %s behind: %v", tmp, err)
	}
}
