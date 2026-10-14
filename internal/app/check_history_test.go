package app_test

import (
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// A check change on a task that was ever claimed is recorded, never refused,
// and surfaced in show, verify complete and list.
func TestCheckHistoryRecordsChangesAfterClaim(t *testing.T) {
	f := newFixture(t)
	id := f.add("a")
	setProof := func(spec string) {
		t.Helper()
		f.apply(plan.UpdateTask{Target: plan.Ref(id), Proof: mustParseProof(t, spec)})
	}

	f.apply(plan.UpdateTask{Target: plan.Ref(id), Proof: mustParseProof(t, "run: test -d ."), Regression: mustParseProof(t, "run: test -d .")})
	if h := f.show(string(id)).CheckHistory; len(h) != 0 {
		t.Fatalf("a never-claimed task must have no history, got %+v", h)
	}

	s := f.claim(string(id))
	setProof("run: test -d . size=medium")
	h := f.show(string(id)).CheckHistory
	if len(h) != 1 {
		t.Fatalf("want one history row, got %+v", h)
	}
	if h[0].Field != task.CheckFieldProof || h[0].Actor != task.CheckActorPlanner || h[0].OldSpec == h[0].NewSpec {
		t.Fatalf("unexpected row %+v", h[0])
	}

	res := f.complete(s)
	if !res.ChecksChangedAfterClaim || len(res.CheckChanges) != 1 {
		t.Fatalf("verify must report the change: %+v", res)
	}

	snap, err := f.e.List(f.ctx, app.ListQuery{ProjectID: f.proj.ID, Filter: app.FilterAll})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range snap.Tasks {
		if v.ID == id && len(v.CheckHistory) == 0 {
			t.Fatalf("list view must carry the history: %+v", v)
		}
	}
}
