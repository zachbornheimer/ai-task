package app_test

import (
	"fmt"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
)

// seedStaggeredCompletions adds n completed tasks t000..t(n-1) whose
// completion times rise with their number, so the newest are the highest.
func (f *fixture) seedStaggeredCompletions(n int) {
	f.t.Helper()
	f.seedCompletedUnderEpic(n)
	f.sql(`UPDATE tasks SET completed_at = 1000000 + CAST(substr(description, 2) AS INTEGER) WHERE kind = 'task' AND description LIKE 't%'`)
}

// TestListCompleteIsBoundedByDefault: the completed history only grows, and
// `atask list complete` on a board of thousands took minutes. It returns
// the newest page and says how many it left out.
func TestListCompleteIsBoundedByDefault(t *testing.T) {
	clearRemoteStore(t)
	f := newFixture(t)
	f.seedStaggeredCompletions(250)
	snap, err := f.e.List(f.ctx, app.ListQuery{ProjectID: f.proj.ID, Filter: app.FilterComplete})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Tasks) != app.DefaultHistoryLimit {
		t.Fatalf("listed %d completed tasks, want the default page of %d", len(snap.Tasks), app.DefaultHistoryLimit)
	}
	if snap.Omitted != 250-app.DefaultHistoryLimit {
		t.Fatalf("omitted = %d, want %d", snap.Omitted, 250-app.DefaultHistoryLimit)
	}
}

func TestListLimitKeepsTheNewestCompletions(t *testing.T) {
	clearRemoteStore(t)
	f := newFixture(t)
	f.seedStaggeredCompletions(30)
	snap, err := f.e.List(f.ctx, app.ListQuery{ProjectID: f.proj.ID, Filter: app.FilterComplete, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, v := range snap.Tasks {
		got[v.Title] = true
	}
	for i := 25; i < 30; i++ {
		if title := fmt.Sprintf("t%03d", i); !got[title] {
			t.Errorf("the newest completions must be kept; %s is missing from %v", title, got)
		}
	}
	if len(snap.Tasks) != 5 || snap.Omitted != 25 {
		t.Fatalf("listed %d, omitted %d; want 5 and 25", len(snap.Tasks), snap.Omitted)
	}
}

func TestListLimitCanBeLifted(t *testing.T) {
	clearRemoteStore(t)
	f := newFixture(t)
	f.seedStaggeredCompletions(250)
	snap, err := f.e.List(f.ctx, app.ListQuery{ProjectID: f.proj.ID, Filter: app.FilterComplete, Limit: app.NoLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Tasks) != 250 || snap.Omitted != 0 {
		t.Fatalf("listed %d, omitted %d; want all 250", len(snap.Tasks), snap.Omitted)
	}
}

// Open work is never paged: a limit that hid a ready task would be a bug.
func TestListOpenIgnoresTheHistoryLimit(t *testing.T) {
	clearRemoteStore(t)
	f := newFixture(t)
	for i := range 12 {
		f.add(fmt.Sprintf("open%02d", i))
	}
	snap, err := f.e.List(f.ctx, app.ListQuery{ProjectID: f.proj.ID, Filter: app.FilterOpen, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Tasks) != 12 || snap.Omitted != 0 {
		t.Fatalf("listed %d open tasks, omitted %d; want all 12", len(snap.Tasks), snap.Omitted)
	}
}
