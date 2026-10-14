package sqlite

import (
	"encoding/json"

	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/proof"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// recordPendingActions replaces a task's action rows with one pending row
// per on-complete action of its contract. It runs inside the completion
// transaction, so a completed task always has its actions on record.
func (t *Tx) recordPendingActions(id task.ID) error {
	var policy string
	if err := t.tx.QueryRowContext(t.ctx, `SELECT policy_json FROM tasks WHERE id = ?`, id).Scan(&policy); err != nil {
		return wrapInternal(err, "read policy for actions")
	}
	pol, err := verification.Parse([]byte(policy))
	if err != nil {
		return wrapInternal(err, "decode policy for actions")
	}
	if _, err := t.tx.ExecContext(t.ctx, `DELETE FROM actions WHERE task_id = ?`, id); err != nil {
		return wrapInternal(err, "clear actions")
	}
	for i, a := range pol.OnComplete {
		opts, err := json.Marshal(a.Options)
		if err != nil {
			return wrapInternal(err, "encode action options")
		}
		if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO actions (task_id, position, preset, subject, options_json, detach, status) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, i, a.Preset, a.Subject, string(opts), a.Detach, proof.ActionPending); err != nil {
			return wrapInternal(err, "record action")
		}
	}
	return nil
}

// Actions lists a task's actions in declaration order.
func (t *Tx) Actions(id task.ID) ([]proof.ActionResult, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT preset, subject, options_json, detach, status, attempts, run_json FROM actions WHERE task_id = ? ORDER BY position`, id)
	if err != nil {
		return nil, wrapInternal(err, "list actions")
	}
	defer discard.CloseReadOnly(rows)
	out := []proof.ActionResult{}
	for rows.Next() {
		var a proof.ActionResult
		var opts, run string
		if err := rows.Scan(&a.Spec.Preset, &a.Spec.Subject, &opts, &a.Spec.Detach, &a.Status, &a.Attempts, &run); err != nil {
			return nil, wrapInternal(err, "scan action")
		}
		if err := json.Unmarshal([]byte(opts), &a.Spec.Options); err != nil || a.Spec.Options == nil {
			a.Spec.Options = map[string]string{}
		}
		if run != "" {
			var r proof.RunResult
			if err := json.Unmarshal([]byte(run), &r); err == nil {
				a.Run = &r
			}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// StartAction marks action position as running and counts the attempt.
func (t *Tx) StartAction(id task.ID, position int) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE actions SET status = ?, attempts = attempts + 1 WHERE task_id = ? AND position = ?`, proof.ActionRunning, id, position)
	return wrapInternal(err, "start action")
}

// FinishAction records how an attempt ended and its RunResult.
func (t *Tx) FinishAction(id task.ID, position int, status proof.ActionStatus, run proof.RunResult) error {
	b, err := json.Marshal(run)
	if err != nil {
		return wrapInternal(err, "encode action run")
	}
	_, err = t.tx.ExecContext(t.ctx, `UPDATE actions SET status = ?, run_json = ? WHERE task_id = ? AND position = ?`, status, string(b), id, position)
	return wrapInternal(err, "finish action")
}
