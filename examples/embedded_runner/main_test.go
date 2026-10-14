package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestRunnerDrainsGraphWithFakeModel runs the whole host flow with four
// workers and no model provider: plan, review, concurrent claims, cohort
// verification, promotion, done.
func TestRunnerDrainsGraphWithFakeModel(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("HK", "0")
	t.Setenv("GIT_AUTHOR_NAME", "at-example")
	t.Setenv("GIT_AUTHOR_EMAIL", "at@example")
	t.Setenv("GIT_COMMITTER_NAME", "at-example")
	t.Setenv("GIT_COMMITTER_EMAIL", "at@example")
	repo := filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(repo, 0o755)
	if err := initRepo(repo); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := run(ctx, repo, filepath.Join(t.TempDir(), "at.db"), 4, os.Stdout); err != nil {
		t.Fatal(err)
	}
	// Every task's file was committed on its task branch; completion leaves
	// the target branch alone.
	for _, f := range []string{"compat.txt", "store.txt", "scopes.txt", "e2e.txt", "rotate.txt", "api.txt", "ui.txt"} {
		out, err := exec.Command("git", "-C", repo, "log", "--all", "--oneline", "--", f).Output()
		if err != nil || len(out) == 0 {
			t.Fatalf("%s was not committed on any task branch: %v", f, err)
		}
	}
}
