package app_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// TestRunLogDirResolvesThroughStore: a run's log directory is found from
// the board's own record of the run, not by looking for a run-<id>
// directory on disk. A run number is unique on one board, but a log root
// shared with an older store holds same-numbered directories of projects
// this board has never heard of (run-2806 existed under two projects).
func TestRunLogDirResolvesThroughStore(t *testing.T) {
	clearRemoteStore(t)
	f := newGitFixture(t)
	id := f.addWith("logged", "test -d .")
	res := f.complete(f.claim(string(id)))
	if res.RunID == 0 || res.LogDir == "" {
		t.Fatalf("completion reported run %d, log dir %q", res.RunID, res.LogDir)
	}
	// A leftover directory with the same run number under a project this
	// board does not have.
	stray := filepath.Join(filepath.Dir(filepath.Dir(res.LogDir)), "proj-not-on-this-board", filepath.Base(res.LogDir))
	if err := os.MkdirAll(stray, 0o750); err != nil {
		t.Fatal(err)
	}
	got, err := f.e.RunLogDir(f.ctx, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got != res.LogDir {
		t.Fatalf("RunLogDir(%d) = %q, want %q", res.RunID, got, res.LogDir)
	}
	if _, err := f.e.RunLogDir(f.ctx, res.RunID+1000); fault.CodeOf(err) != fault.CodeNotFound {
		t.Fatalf("an unknown run: err = %v, want NOT_FOUND", err)
	}
}
