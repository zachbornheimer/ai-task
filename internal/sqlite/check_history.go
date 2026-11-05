package sqlite

import (
	"encoding/json"
	"time"

	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// AppendCheckChange records one change to a task's checks.
func (t *Tx) AppendCheckChange(id task.ID, c task.CheckChange) error {
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO check_history (task_id, changed_at, field, old_spec, new_spec, actor, cwd) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, ms(c.ChangedAt), c.Field, c.OldSpec, c.NewSpec, c.Actor, c.Cwd)
	return wrapInternal(err, "record check change")
}

// CheckChanges lists a task's recorded check changes made at or after since,
// oldest first. A zero since lists them all.
func (t *Tx) CheckChanges(id task.ID, since time.Time) ([]task.CheckChange, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT changed_at, field, old_spec, new_spec, actor, cwd FROM check_history WHERE task_id = ? AND changed_at >= ? ORDER BY id`, id, ms(since))
	if err != nil {
		return nil, wrapInternal(err, "list check changes")
	}
	defer discard.CloseReadOnly(rows)
	out := []task.CheckChange{}
	for rows.Next() {
		var c task.CheckChange
		var at int64
		if err := rows.Scan(&at, &c.Field, &c.OldSpec, &c.NewSpec, &c.Actor, &c.Cwd); err != nil {
			return nil, wrapInternal(err, "scan check change")
		}
		c.ChangedAt = fromMS(at)
		out = append(out, c)
	}
	return out, wrapInternal(rows.Err(), "read check changes")
}

// checkChangesForQuery is the set-based check-history load: the listed ids
// travel as one bound JSON array, so the query is one constant per engine
// and every value is a bound parameter. SQLite reads the array with
// json_each, PostgreSQL with jsonb_array_elements_text.
func (t *Tx) checkChangesForQuery() string {
	if t.d.isPostgres() {
		return `SELECT task_id, changed_at, field, old_spec, new_spec, actor, cwd FROM check_history WHERE task_id IN (SELECT value FROM jsonb_array_elements_text(?::jsonb)) ORDER BY id`
	}
	return `SELECT task_id, changed_at, field, old_spec, new_spec, actor, cwd FROM check_history WHERE task_id IN (SELECT value FROM json_each(?)) ORDER BY id`
}

// CheckChangesFor lists every recorded check change of the given tasks,
// oldest first, in one query instead of one query per task. A listing
// loads its whole board this way; CheckChanges stays the single-task
// path. Every given task maps to a slice, empty when it has no recorded
// changes.
func (t *Tx) CheckChangesFor(ids []task.ID) (map[task.ID][]task.CheckChange, error) {
	out := make(map[task.ID][]task.CheckChange, len(ids))
	for _, id := range ids {
		out[id] = []task.CheckChange{}
	}
	if len(ids) == 0 {
		return out, nil
	}
	payload, err := json.Marshal(ids)
	if err != nil {
		return nil, wrapInternal(err, "encode listed ids")
	}
	rows, err := t.tx.QueryContext(t.ctx, t.checkChangesForQuery(), string(payload))
	if err != nil {
		return nil, wrapInternal(err, "list check changes")
	}
	for rows.Next() {
		var id task.ID
		var c task.CheckChange
		var at int64
		if err := rows.Scan(&id, &at, &c.Field, &c.OldSpec, &c.NewSpec, &c.Actor, &c.Cwd); err != nil {
			discard.CloseReadOnly(rows)
			return nil, wrapInternal(err, "scan check change")
		}
		c.ChangedAt = fromMS(at)
		out[id] = append(out[id], c)
	}
	err = wrapInternal(rows.Err(), "read check changes")
	discard.CloseReadOnly(rows)
	if err != nil {
		return nil, err
	}
	return out, nil
}
