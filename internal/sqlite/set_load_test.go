package sqlite

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/must"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// TestSetLoadsMatchThePerTaskQueries proves the two loaders a listing uses
// to avoid a round trip per row return exactly what the per-task queries
// return: the same rows in the same order, and an entry for every listed
// task—empty when it has none, so a caller never falls back to a query.
func TestSetLoadsMatchThePerTaskQueries(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "at.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must.OK(t, s.Close()) }()
	at := int64(1_700_000_000_000)
	seed := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	seed(`INSERT INTO projects (id, name, root_path, created_at, updated_at) VALUES ('proj-set', 'set', NULL, ?, ?)`, at, at)
	ids := []task.ID{"at-t1", "at-t2", "at-t3", "at-t4"}
	for _, id := range ids {
		seed(`INSERT INTO tasks (id, project_id, kind, description, outcome, policy_json, policy_digest, failures, created_at, updated_at)
			VALUES (?, 'proj-set', 'task', ?, ?, '{}', 'digest', 0, ?, ?)`, id, "task "+string(id), "outcome", at, at)
	}
	// t1 requires t2 and t3 with one created_at, so requires_id orders
	// them; t3 requires t1 later; t2 and t4 require nothing.
	seed(`INSERT INTO task_dependencies (task_id, requires_id, created_at) VALUES ('at-t1', 'at-t3', ?), ('at-t1', 'at-t2', ?)`, at, at)
	seed(`INSERT INTO task_dependencies (task_id, requires_id, created_at) VALUES ('at-t3', 'at-t1', ?)`, at+1000)
	seed(`INSERT INTO check_history (id, task_id, changed_at, field, old_spec, new_spec, actor, cwd) VALUES
		(1, 'at-t1', ?, 'proof', 'a', 'b', 'planner', '/tmp'),
		(2, 'at-t1', ?, 'regression', 'c', 'd', 'worker', '/tmp'),
		(3, 'at-t3', ?, 'proof', 'e', 'f', 'planner', '/tmp')`, at+10, at+20, at+30)

	wantChanges := map[task.ID][]task.CheckChange{}
	wantReqs := map[task.ID][]task.ID{}
	must.OK(t, s.Read(ctx, func(tx *Tx) error {
		for _, id := range ids {
			cs, err := tx.CheckChanges(id, time.Time{})
			if err != nil {
				return err
			}
			wantChanges[id] = cs
			rs, err := tx.RequirementIDs(id)
			if err != nil {
				return err
			}
			wantReqs[id] = rs
		}
		return nil
	}))
	if len(wantChanges["at-t1"]) != 2 || len(wantReqs["at-t1"]) != 2 {
		t.Fatalf("seed did not load as expected: %d changes, %d requirements", len(wantChanges["at-t1"]), len(wantReqs["at-t1"]))
	}
	must.OK(t, s.Read(ctx, func(tx *Tx) error {
		gotChanges, err := tx.CheckChangesFor(ids)
		if err != nil {
			return err
		}
		gotReqs, err := tx.RequirementIDsFor(ids)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if !reflect.DeepEqual(gotChanges[id], wantChanges[id]) {
				t.Errorf("CheckChangesFor(%s) = %+v; per-task query returned %+v", id, gotChanges[id], wantChanges[id])
			}
			if !reflect.DeepEqual(gotReqs[id], wantReqs[id]) {
				t.Errorf("RequirementIDsFor(%s) = %+v; per-task query returned %+v", id, gotReqs[id], wantReqs[id])
			}
		}
		return nil
	}))
}

// TestSetLoadsTakeAnyNumberOfIDs: the loads carry the listed ids as one
// bound array, so a board larger than any parameter limit still loads in
// one statement per relation.
func TestSetLoadsTakeAnyNumberOfIDs(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "at.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must.OK(t, s.Close()) }()
	const n = 1000
	at := int64(1_700_000_000_000)
	ids := make([]task.ID, n)
	must.OK(t, s.Write(ctx, func(tx *Tx) error {
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO projects (id, name, root_path, created_at, updated_at) VALUES ('proj-many', 'many', NULL, ?, ?)`, at, at); err != nil {
			return err
		}
		for i := range n {
			ids[i] = task.ID("at-many-" + pad(i))
			if _, err := tx.tx.ExecContext(ctx, `INSERT INTO tasks (id, project_id, kind, description, outcome, policy_json, policy_digest, failures, created_at, updated_at)
				VALUES (?, 'proj-many', 'task', ?, ?, '{}', 'digest', 0, ?, ?)`, ids[i], "task", "outcome", at, at); err != nil {
				return err
			}
			if _, err := tx.tx.ExecContext(ctx, `INSERT INTO check_history (task_id, changed_at, field, old_spec, new_spec, actor, cwd) VALUES (?, ?, 'proof', 'a', 'b', 'planner', '/tmp')`, ids[i], at); err != nil {
				return err
			}
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO task_dependencies (task_id, requires_id, created_at) VALUES (?, ?, ?)`, ids[n-1], ids[0], at); err != nil {
			return err
		}
		return nil
	}))
	must.OK(t, s.Read(ctx, func(tx *Tx) error {
		changes, err := tx.CheckChangesFor(ids)
		if err != nil {
			return err
		}
		reqs, err := tx.RequirementIDsFor(ids)
		if err != nil {
			return err
		}
		if len(changes) != n || len(reqs) != n {
			t.Errorf("set loads covered %d changes and %d requirement lists; want %d each", len(changes), len(reqs), n)
		}
		total := 0
		for _, cs := range changes {
			total += len(cs)
		}
		if total != n {
			t.Errorf("set load returned %d check changes; want %d", total, n)
		}
		if len(reqs[ids[n-1]]) != 1 || len(reqs[ids[0]]) != 0 {
			t.Errorf("set load returned %d requirements for the dependent task and %d for the required one; want 1 and 0", len(reqs[ids[n-1]]), len(reqs[ids[0]]))
		}
		return nil
	}))
}

// pad renders i as a fixed-width decimal so the generated ids sort as they
// are created.
func pad(i int) string {
	const width = 4
	out := []byte("0000")
	for pos := width - 1; pos >= 0 && i > 0; pos-- {
		out[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(out)
}
