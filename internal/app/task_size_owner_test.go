package app_test

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/proof"
)

const (
	sizeColumnPresent = `SELECT count(*) FROM pragma_table_info('tasks') WHERE name = 'size'`
	sizeMigration     = 21
)

func jsonKeys(t *testing.T, v any) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	return keys
}

// groupProofBudget verifies a one-child group whose proof carries size
// and returns the budget its proof run got.
func groupProofBudget(t *testing.T, size proof.Size) time.Duration {
	t.Helper()
	f := newGitFixture(t)
	gp := &proof.ProofSpec{Preset: "run", Subject: "test -d .", Options: map[string]string{}, Size: size}
	res := f.apply(
		plan.AddGroup{Key: "g", Title: "g", Proof: gp},
		plan.AddTask{Key: "a", Title: "a", Parent: "g", TaskChecks: okChecks()},
	)
	f.completeWith(res.Created["a"], "a.txt", "a\n")
	out, err := f.e.VerifyGroup(f.ctx, f.proj.ID, string(res.Created["g"]))
	if err != nil || !out.Completed {
		t.Fatalf("group verify: %+v %v", out, err)
	}
	for i := range out.Evidence {
		if run := out.Evidence[i].Run; run != nil && run.CheckType == proof.CheckProof {
			return run.Budget()
		}
	}
	t.Fatalf("no proof run in %+v", out.Evidence)
	return 0
}

// TestTaskSizeHasOneOwner: size belongs to the check. No task or group
// view carries a size; a group's budget is its proof's size=; a task stored
// with a task-level size keeps its old regression budget through migration.
func TestTaskSizeHasOneOwner(t *testing.T) {
	t.Run("views carry no size", func(t *testing.T) {
		f := newGitFixture(t)
		res := f.apply(plan.AddGroup{Key: "g", Title: "g", TaskChecks: okChecks()}, plan.AddTask{Key: "a", Title: "a", TaskChecks: okChecks()})
		if _, ok := jsonKeys(t, f.show(string(res.Created["a"])))["size"]; ok {
			t.Fatal("show task has a top-level size")
		}
		if _, ok := jsonKeys(t, f.show(string(res.Created["g"])))["size"]; ok {
			t.Fatal("show group has a top-level size")
		}
		snap, err := f.e.List(f.ctx, app.ListQuery{ProjectID: f.proj.ID, Filter: app.FilterAll})
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range snap.Groups {
			if _, ok := jsonKeys(t, g)["size"]; ok {
				t.Fatal("list group has a top-level size")
			}
		}
	})

	t.Run("group budget reads the proof size", func(t *testing.T) {
		for size, want := range map[proof.Size]time.Duration{proof.SizeLarge: 900 * time.Second, proof.SizeSmall: 30 * time.Second} {
			if got := groupProofBudget(t, size); got > want || got < want-5*time.Second {
				t.Errorf("group proof size %s budget = %s, want about %s", size, got, want)
			}
		}
	})

	t.Run("migration keeps a stored task's budget", func(t *testing.T) {
		f := newGitFixture(t)
		id := f.apply(plan.AddTask{Key: "old", Title: "old", TaskChecks: okChecks()}).Created["old"]
		f.sql(`DELETE FROM schema_migrations WHERE version = ?`, sizeMigration)
		f.sql(`UPDATE tasks SET policy_json = ? WHERE id = ?`, `{"proof":{"preset":"run","subject":"test -d .","options":{}},"regression_proof":{"preset":"run","subject":"true","options":{}}}`, id)
		f.restoreSizeColumn(string(id), "large")
		f.e = f.open()
		reg := f.show(string(id)).Regression
		if reg == nil || reg.Size != proof.SizeLarge {
			t.Fatalf("regression after migration = %+v, want size large", reg)
		}
		if p := f.show(string(id)).Proof; p == nil || p.Size == proof.SizeLarge {
			t.Fatalf("proof after migration = %+v, want it to stay small", p)
		}
	})
}

// restoreSizeColumn recreates the pre-migration tasks.size column holding
// size for the task, as a database from before the column was dropped has it.
func (f *fixture) restoreSizeColumn(id, size string) {
	f.t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	defer discard.CloseReadOnly(db)
	var n int
	if err := db.QueryRow(sizeColumnPresent).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	if n == 0 {
		if _, err := db.Exec(`ALTER TABLE tasks ADD COLUMN size TEXT NOT NULL DEFAULT 'small'`); err != nil {
			f.t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE tasks SET size = ? WHERE id = ?`, size, id); err != nil {
		f.t.Fatal(err)
	}
}
