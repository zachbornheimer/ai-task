package sqlite

import (
	"time"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// InsertLog appends an entry and returns its global sequence number.
func (t *Tx) InsertLog(id task.ID, attemptID int64, e execution.LogEntry, now time.Time) (int64, error) {
	res, err := t.tx.ExecContext(t.ctx, `INSERT INTO task_log_entries (task_id, attempt_id, done, next, learned, note, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, attemptID, e.Done, e.Next, e.Learned, e.Note, ms(now))
	if err != nil {
		return 0, wrapInternal(err, "insert log entry")
	}
	seq, _ := res.LastInsertId()
	return seq, nil
}

const logSelect = `SELECT l.id, a.seq, l.created_at, l.done, l.next, l.learned, l.note
	FROM task_log_entries l JOIN execution_attempts a ON a.id = l.attempt_id WHERE l.task_id = ?`

func scanLogs(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]execution.RecordedLog, error) {
	var out []execution.RecordedLog
	for rows.Next() {
		var l execution.RecordedLog
		var at int64
		if err := rows.Scan(&l.Seq, &l.AttemptSeq, &at, &l.Done, &l.Next, &l.Learned, &l.Note); err != nil {
			return nil, wrapInternal(err, "scan log entry")
		}
		l.At = fromMS(at)
		out = append(out, l)
	}
	return out, rows.Err()
}

// Logs returns a page of history in chronological order.
func (t *Tx) Logs(id task.ID, limit, offset int) ([]execution.RecordedLog, error) {
	rows, err := t.tx.QueryContext(t.ctx, logSelect+` ORDER BY l.id LIMIT ? OFFSET ?`, id, limit, offset)
	if err != nil {
		return nil, wrapInternal(err, "list logs")
	}
	defer rows.Close()
	return scanLogs(rows)
}

// LogCount returns the number of entries for a task.
func (t *Tx) LogCount(id task.ID) (int, error) {
	var n int
	err := t.tx.QueryRowContext(t.ctx, `SELECT count(*) FROM task_log_entries WHERE task_id = ?`, id).Scan(&n)
	return n, wrapInternal(err, "count logs")
}

// Handoff assembles the bounded context a new attempt starts from, using
// three indexed queries instead of loading the whole history.
func (t *Tx) Handoff(id task.ID) (execution.Handoff, error) {
	var h execution.Handoff
	var err error
	if h.TotalLogs, err = t.LogCount(id); err != nil {
		return h, err
	}
	if h.TotalLogs == 0 {
		return h, nil
	}
	rows, err := t.tx.QueryContext(t.ctx, logSelect+` ORDER BY l.id DESC LIMIT ?`, id, execution.RecentLogLimit)
	if err != nil {
		return h, wrapInternal(err, "recent logs")
	}
	h.RecentLogs, err = scanLogs(rows)
	rows.Close()
	if err != nil {
		return h, err
	}
	err = t.tx.QueryRowContext(t.ctx, `SELECT next FROM task_log_entries WHERE task_id = ? AND next <> '' ORDER BY id DESC LIMIT 1`, id).Scan(&h.LatestNext)
	if err != nil && !isNoRows(err) {
		return h, wrapInternal(err, "latest next")
	}
	lrows, err := t.tx.QueryContext(t.ctx, `SELECT learned FROM task_log_entries WHERE task_id = ? AND learned <> '' ORDER BY id DESC LIMIT ?`, id, execution.LearningLimit)
	if err != nil {
		return h, wrapInternal(err, "learnings")
	}
	defer lrows.Close()
	for lrows.Next() {
		var s string
		if err := lrows.Scan(&s); err != nil {
			return h, wrapInternal(err, "scan learning")
		}
		h.Learnings = append(h.Learnings, s)
	}
	if err := lrows.Err(); err != nil {
		return h, wrapInternal(err, "learnings")
	}
	h.Warnings = h.ComputeWarnings()
	return h, nil
}
