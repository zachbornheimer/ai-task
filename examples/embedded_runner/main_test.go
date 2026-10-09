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
	// Every task's file was promoted into main.
	for _, f := range []string{"compat.txt", "store.txt", "scopes.txt", "e2e.txt", "rotate.txt", "api.txt", "ui.txt"} {
		if _, err := os.Stat(filepath.Join(repo, f)); err != nil {
			t.Fatalf("%s not promoted", f)
		}
	}
}
