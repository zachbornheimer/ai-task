package testgit_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/testgit"
)

func TestCeilingStopsParentRepoAdoption(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tempRoot := filepath.Join(root, "child")
	work := filepath.Join(tempRoot, "work")
	if err := os.MkdirAll(work, 0o750); err != nil {
		t.Fatal(err)
	}
	testgit.Run(t, root, "init", "-q")
	t.Setenv("TMPDIR", tempRoot)

	env, err := testgit.Overrides(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	toplevel := func() (string, error) {
		cmd := exec.Command("git", "rev-parse", "--show-toplevel")
		cmd.Dir = work
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	if got, err := toplevel(); err == nil {
		t.Fatalf("adopted parent repo %q from %s", got, work)
	}
	initCmd := exec.Command("git", "init", "-q")
	initCmd.Dir = work
	initCmd.Env = append(os.Environ(), env...)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	got, err := toplevel()
	if err != nil || got != work {
		t.Fatalf("toplevel = %q, %v; want %q", got, err, work)
	}
}
