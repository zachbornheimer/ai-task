package sqlite

import (
	"context"
	"time"

	"github.com/zachbornheimer/ai-task/internal/dependency"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// Exists implements dependency.Checker.
func (t *Tx) Exists(_ context.Context, e dependency.Edge) (bool, error) {
	var n int
	err := t.tx.QueryRowContext(t.ctx, `SELECT count(*) FROM task_dependencies WHERE task_id = ? AND requires_id = ?`, e.Task, e.Requires).Scan(&n)
	return n > 0, wrapInternal(err, "check edge")
}

// Reaches implements dependency.Checker: does `from` transitively require
// `to`? Equivalently, is `from` among the transitive dependents of `to`?
// The query walks dependents of `to` (who requires it, recursively) via the
// task_dependencies_requires index. That direction is chosen because tasks
// are usually created in topological order and an edge is usually added to a
// new task with no dependents yet, making the walk empty; walking the
// prerequisite's ancestors would instead visit the whole upstream graph on
// every insert. SQLite evaluates recursive CTEs lazily, so the outer LIMIT 1
// stops the walk as soon as a cycle is found.
func (t *Tx) Reaches(_ context.Context, from, to task.ID) (bool, error) {
	var n int
	err := t.tx.QueryRowContext(t.ctx, `
		WITH RECURSIVE dependents(id) AS (
			SELECT task_id FROM task_dependencies WHERE requires_id = ?
			UNION
			SELECT d.task_id FROM task_dependencies d JOIN dependents ON d.requires_id = dependents.id
		)
		SELECT 1 FROM dependents WHERE id = ? LIMIT 1`, to, from).Scan(&n)
	if isNoRows(err) {
		return false, nil
	}
	return n == 1, wrapInternal(err, "reachability query")
}

// InsertEdge records a validated edge.
func (t *Tx) InsertEdge(e dependency.Edge, now time.Time) error {
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO task_dependencies (task_id, requires_id, created_at) VALUES (?, ?, ?)`, e.Task, e.Requires, ms(now))
	return wrapInternal(err, "insert edge")
}

// DeleteEdge removes an edge, reporting whether it existed.
func (t *Tx) DeleteEdge(e dependency.Edge) (bool, error) {
	res, err := t.tx.ExecContext(t.ctx, `DELETE FROM task_dependencies WHERE task_id = ? AND requires_id = ?`, e.Task, e.Requires)
	if err != nil {
		return false, wrapInternal(err, "delete edge")
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Neighbor is a directly related task as shown in views.
type Neighbor struct {
	Record Record
}

// RequirementIDs returns the IDs of the tasks `id` requires.
func (t *Tx) RequirementIDs(id task.ID) ([]task.ID, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT requires_id FROM task_dependencies WHERE task_id = ? ORDER BY created_at, requires_id`, id)
	if err != nil {
		return nil, wrapInternal(err, "list requirements")
	}
	defer rows.Close()
	var out []task.ID
	for rows.Next() {
		var n task.ID
		if err := rows.Scan(&n); err != nil {
			return nil, wrapInternal(err, "scan requirement")
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Requirements returns the tasks `id` requires, with their status facts.
func (t *Tx) Requirements(id task.ID) ([]Record, error) {
	return t.neighbors(`SELECT requires_id FROM task_dependencies WHERE task_id = ? ORDER BY created_at, requires_id`, id)
}

// Dependents returns the tasks that require `id`.
func (t *Tx) Dependents(id task.ID) ([]Record, error) {
	return t.neighbors(`SELECT task_id FROM task_dependencies WHERE requires_id = ? ORDER BY created_at, task_id`, id)
}

func (t *Tx) neighbors(q string, id task.ID) ([]Record, error) {
	rows, err := t.tx.QueryContext(t.ctx, q, id)
	if err != nil {
		return nil, wrapInternal(err, "list neighbors")
	}
	var ids []task.ID
	for rows.Next() {
		var n task.ID
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, wrapInternal(err, "scan neighbor")
		}
		ids = append(ids, n)
	}
	rows.Close()
	out := make([]Record, 0, len(ids))
	for _, n := range ids {
		r, err := t.GetRecord(n)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// ProjectEdges returns every edge in a project.
func (t *Tx) ProjectEdges(pid project.ID) ([]dependency.Edge, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT d.task_id, d.requires_id FROM task_dependencies d JOIN tasks t ON t.id = d.task_id WHERE t.project_id = ? ORDER BY d.task_id, d.requires_id`, pid)
	if err != nil {
		return nil, wrapInternal(err, "list edges")
	}
	defer rows.Close()
	var out []dependency.Edge
	for rows.Next() {
		var e dependency.Edge
		if err := rows.Scan(&e.Task, &e.Requires); err != nil {
			return nil, wrapInternal(err, "scan edge")
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
