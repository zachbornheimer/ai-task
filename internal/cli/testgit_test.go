package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/testgit"
)

// A user's global config with commit signing must not reach test repositories.
func TestTestGitNeverSigns(t *testing.T) {
	home := t.TempDir()
	global := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(global, []byte("[commit]\n\tgpgsign = true\n[user]\n\tsigningkey = NOPE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", global)

	start := time.Now()
	dir := t.TempDir()
	testgit.Run(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	testgit.Run(t, dir, "add", "a.txt")
	testgit.Run(t, dir, "commit", "-q", "-m", "one")
	if got := testgit.Run(t, dir, "log", "-1", "--format=%G?"); got != "N" {
		t.Fatalf("commit signature status = %q, want N", got)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("git took %s, want under 10s", d)
	}
}

func TestMain(m *testing.M) { os.Exit(testgit.IsolateMain(m)) }
