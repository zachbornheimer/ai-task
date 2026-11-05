package app_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/must"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/sqlite/stmtcount"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// clearRemoteStore keeps a test off the production board: an engine opened
// without an explicit path would otherwise follow ATASK_DB_URL.
func clearRemoteStore(t *testing.T) {
	t.Helper()
	t.Setenv("ATASK_DB_URL", "")
}

// openCounting opens a second engine on the fixture's store whose SQLite
// driver reports every statement to the returned counter.
func (f *fixture) openCounting(name string) (*app.Engine, *stmtcount.Counter) {
	f.t.Helper()
	counter := stmtcount.Register(name)
	e, err := app.Open(f.ctx, app.Config{Path: f.path, Now: f.c.Now, PollInterval: 50 * time.Millisecond, StoreDriver: name})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { must.OK(f.t, e.Close()) })
	return e, counter
}

// TestOpenSkipsMigrationWriteWhenCurrent: opening a store whose ledger
// already records every migration this build embeds must not write, so
// every CLI call on a migrated board pays one read instead of a schema
// statement plus a write transaction.
func TestOpenSkipsMigrationWriteWhenCurrent(t *testing.T) {
	clearRemoteStore(t)
	f := newFixture(t) // migrates f.path
	name := "count-open-current"
	counter := stmtcount.Register(name)
	e, err := app.Open(f.ctx, app.Config{Path: f.path, StoreDriver: name})
	if err != nil {
		t.Fatal(err)
	}
	must.OK(t, e.Close())
	if writes := counter.Writes(); len(writes) != 0 {
		t.Fatalf("opening a current store wrote: %q", writes)
	}
	if n := counter.WriteTransactions(); n != 0 {
		t.Fatalf("opening a current store began %d write transactions, want 0", n)
	}
}

// seedCompletedUnderEpic adds n tasks under one open epic and marks each
// complete, the shape of `list complete` on a real board: every row's
// parent is a group the listing does not itself return.
func (f *fixture) seedCompletedUnderEpic(n int) {
	f.t.Helper()
	ops := []plan.Change{plan.AddGroup{Key: "epic", Title: "epic"}}
	for i := range n {
		key := fmt.Sprintf("t%03d", i)
		ops = append(ops, plan.AddTask{Key: key, Title: key, Parent: "epic", TaskChecks: []verification.CheckSpec{check("u-"+key, "test -d .", true)}})
	}
	f.apply(ops...)
	f.sql(`UPDATE tasks SET completed_at = ? WHERE kind = 'task' AND parent_id <> ''`, f.c.Now().UnixMilli())
}

// TestListQueryCountIsConstant pins the cost of a listing to the number of
// relations it reads, not the number of rows it returns: on a remote board
// every statement is a network round trip, so a per-row query turns a
// listing of thousands of completed tasks into minutes.
func TestListQueryCountIsConstant(t *testing.T) {
	clearRemoteStore(t)
	counts := map[int]int{}
	for _, n := range []int{20, 200} {
		f := newFixture(t)
		f.seedCompletedUnderEpic(n)
		e, counter := f.openCounting(fmt.Sprintf("count-list-%d", n))
		counter.Reset()
		snap, err := e.List(f.ctx, app.ListQuery{ProjectID: f.proj.ID, Filter: app.FilterComplete})
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.Tasks) != n {
			t.Fatalf("listed %d completed tasks, want %d", len(snap.Tasks), n)
		}
		counts[n] = counter.Statements()
	}
	if counts[20] != counts[200] {
		t.Fatalf("statements per listing grow with rows: %d for 20 tasks, %d for 200", counts[20], counts[200])
	}
}
