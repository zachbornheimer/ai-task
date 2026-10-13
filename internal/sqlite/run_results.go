package sqlite

import (
	"database/sql"
	"encoding/json"

	"github.com/zachbornheimer/ai-task/internal/proof"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

const runResultColumns = `check_type, preset, subject, options_json, started_at, elapsed_ms, budget_ms, timed_out, return_code, passed, output`

// InsertRunResult records the RunResult of the check that produced evidence
// evidenceID. Task and attempt come from the verification run.
func (t *Tx) InsertRunResult(runID, evidenceID int64, r proof.RunResult) error {
	opts, err := json.Marshal(r.Options)
	if err != nil {
		return wrapInternal(err, "encode run result options")
	}
	var code any
	if r.ReturnCode != nil {
		code = *r.ReturnCode
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO run_results
		(task_id, attempt_id, run_id, evidence_id, `+runResultColumns+`)
		SELECT task_id, attempt_id, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ? FROM verification_runs WHERE id = ?`,
		runID, evidenceID, r.CheckType, r.Preset, r.Subject, string(opts), ms(r.StartedAt), r.ElapsedMS, r.BudgetMS, r.TimedOut, code, r.Passed, r.Output, runID)
	return wrapInternal(err, "insert run result")
}

func scanRunResult(sc interface{ Scan(...any) error }) (proof.RunResult, error) {
	var r proof.RunResult
	var opts string
	var started int64
	var code sql.NullInt64
	if err := sc.Scan(&r.CheckType, &r.Preset, &r.Subject, &opts, &started, &r.ElapsedMS, &r.BudgetMS, &r.TimedOut, &code, &r.Passed, &r.Output); err != nil {
		return r, err
	}
	r.StartedAt = fromMS(started)
	if err := json.Unmarshal([]byte(opts), &r.Options); err != nil || r.Options == nil {
		r.Options = map[string]string{}
	}
	if code.Valid {
		c := int(code.Int64)
		r.ReturnCode = &c
	}
	return r, nil
}

// RunResultsForTask lists every RunResult of a task, oldest first.
func (t *Tx) RunResultsForTask(id string) ([]proof.RunResult, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT `+runResultColumns+` FROM run_results WHERE task_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, wrapInternal(err, "list run results")
	}
	defer rows.Close()
	out := []proof.RunResult{}
	for rows.Next() {
		r, err := scanRunResult(rows)
		if err != nil {
			return nil, wrapInternal(err, "scan run result")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// attachRunResults sets Run on each evidence row that has a RunResult.
func (t *Tx) attachRunResults(runID int64, ev []verification.Evidence) error {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT evidence_id, `+runResultColumns+` FROM run_results WHERE run_id = ?`, runID)
	if err != nil {
		return wrapInternal(err, "list run results for run")
	}
	defer rows.Close()
	byEvidence := map[int64]proof.RunResult{}
	for rows.Next() {
		var eid int64
		// evidence_id leads the row; scanRunResult takes the rest.
		r, err := scanRunResult(prefixed{rows, &eid})
		if err != nil {
			return wrapInternal(err, "scan run result for run")
		}
		byEvidence[eid] = r
	}
	for i := range ev {
		if r, ok := byEvidence[ev[i].ID]; ok {
			ev[i].Run = &r
		}
	}
	return rows.Err()
}

// prefixed scans one leading column before delegating the rest.
type prefixed struct {
	rows interface{ Scan(...any) error }
	head any
}

func (p prefixed) Scan(dest ...any) error { return p.rows.Scan(append([]any{p.head}, dest...)...) }
