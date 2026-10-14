package sqlite

import (
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
