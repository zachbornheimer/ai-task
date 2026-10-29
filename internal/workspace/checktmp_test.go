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
	if short == long || short != CheckTmp("/w") {
		t.Errorf("TMPDIR must be stable per dir and differ between dirs: %q %q", short, long)
	}
}

func TestEnsureCheckTmpRefusesSharedRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0o750); err != nil { // group-accessible: not private
		t.Fatal(err)
	}
	t.Setenv(CheckTmpRootEnv, root)
	if tmp, err := EnsureCheckTmp("/w"); err == nil {
		t.Fatalf("EnsureCheckTmp accepted shared root %s and returned %s", root, tmp)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	tmp, err := EnsureCheckTmp("/w")
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(tmp); err != nil || !info.IsDir() {
		t.Fatalf("EnsureCheckTmp returned %s, not a directory: %v", tmp, err)
	}
	RemoveCheckTmp("/w")
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("RemoveCheckTmp left %s behind", tmp)
	}
}
