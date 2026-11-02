package app_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/must"
)

// remoteBoardEnv names the shared PostgreSQL board the remote test lists.
// Without it the test skips: the defect it covers is a round trip per row,
// which a local file answers for microseconds and a remote store answers
// in tens of milliseconds—invisible locally, minutes for a real board.
const remoteBoardEnv = "ATASK_TEST_POSTGRES_URL"

// TestRemoteBoardListsWithinItsBudget is the regression for the listing
// that never returned: against a real remote board a listing must finish
// inside the caller's own deadline—not run for minutes—and it must list
// the real board, not an empty one.
func TestRemoteBoardListsWithinItsBudget(t *testing.T) {
	raw := os.Getenv(remoteBoardEnv)
	if raw == "" {
		t.Skipf("set %s to a reachable PostgreSQL URL to run the remote-board test", remoteBoardEnv)
	}
	// The value must name a PostgreSQL URL: a path from the environment
	// would silently open a local file and make this meaningless.
	if !strings.HasPrefix(raw, "postgres://") && !strings.HasPrefix(raw, "postgresql://") {
		t.Fatalf("%s must be a postgres:// or postgresql:// URL", remoteBoardEnv)
	}
	ctx := context.Background()
	e, err := app.Open(ctx, app.Config{Path: raw})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must.OK(t, e.Close()) }()
	// The caller's deadline is the contract: a listing that outlives it is
	// the hang this test exists for. It stays well under the acceptance
	// command's 60s and far under the base implementation's minutes.
	listCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	start := time.Now()
	snap, err := e.List(listCtx, app.ListQuery{})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("listing the remote board failed after %s: %v", elapsed.Round(time.Millisecond), err)
	}
	rows := len(snap.Tasks) + len(snap.Groups)
	if rows < 50 {
		t.Fatalf("the remote board listed %d rows in %s; the shared board holds hundreds, so this did not read it", rows, elapsed.Round(time.Millisecond))
	}
	t.Logf("listed %d rows in %s", rows, elapsed.Round(time.Millisecond))
}
