package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/must"
)

func TestVerifyWritesRunLogsPrunesOldRuns(t *testing.T) {
	proj := filepath.Join(t.TempDir(), "proj")
	for i := 1; i <= keepRunLogs+5; i++ {
		must.OK(t, os.MkdirAll(filepath.Join(proj, fmt.Sprintf("run-%d", i)), 0o750))
	}
	must.OK(t, os.MkdirAll(filepath.Join(proj, "unrelated"), 0o750))
	f, path := openCheckLog(filepath.Join(proj, fmt.Sprintf("run-%d", keepRunLogs+6)), "go test/./...")
	if f == nil {
		t.Fatal("no log file")
	}
	discard.CloseReadOnly(f)
	if filepath.Base(path) != "go_test_._....log" {
		t.Fatalf("log name %q", filepath.Base(path))
	}
	entries, _ := os.ReadDir(proj)
	if len(entries) != keepRunLogs+1 {
		t.Fatalf("%d entries after pruning, want %d runs and the unrelated directory", len(entries), keepRunLogs)
	}
	// run-1 … run-6 are the oldest beyond the limit; run-7 is the oldest kept.
	if _, err := os.Stat(filepath.Join(proj, "run-6")); err == nil {
		t.Fatal("run beyond the limit kept")
	}
	if _, err := os.Stat(filepath.Join(proj, "run-7")); err != nil {
		t.Fatal("a run within the limit was pruned")
	}
	// A check run twice in one run (the revision, then the merge) keeps both.
	f2, second := openCheckLog(filepath.Dir(path), "go test/./...")
	if f2 == nil || second == path {
		t.Fatalf("second log %q", second)
	}
	discard.CloseReadOnly(f2)
}
