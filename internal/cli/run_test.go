package cli

import (
	"path/filepath"
	"testing"
)

func TestRunShowOfAnUnknownRunIsNotFound(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	args := []string{"--json", "run", "show", "4242"}
	wantErrorCode(t, atask(t, db, t.TempDir(), args...), "NOT_FOUND", args...)
}

func TestRunShowNeedsARunId(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	for _, args := range [][]string{{"--json", "run"}, {"--json", "run", "show"}, {"--json", "run", "show", "abc"}} {
		if env := atask(t, db, t.TempDir(), args...); env.OK {
			t.Fatalf("atask %v succeeded, want an error", args)
		}
	}
}
