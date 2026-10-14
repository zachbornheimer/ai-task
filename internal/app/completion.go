package app

import (
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// fault is the test seam for crash injection: a non-nil error at a point
// makes the operation stop exactly there, as a killed process would.
func (e *Engine) fault(point string) error {
	if e.faults == nil {
		return nil
	}
	return e.faults(point)
}

// completionSpec is what one completion pins: the contract, regression and
// submission the checks ran against.
type completionSpec struct {
	proj        project.Project
	taskID      task.ID
	attemptID   int64 // single-task
	runID       int64
	submission  int64
	owner       string // token digest (attempt or job owner)
	contractRev int
	regression  string // digest pinned at preparation
	policy      verification.Policy
	source      string // the task's submitted revision
}

// recheckForCompletion re-reads the task inside tx and refuses when
// anything the completion depends on changed since preparation.
func (e *Engine) recheckForCompletion(tx *sqlite.Tx, s completionSpec, final string) (sqlite.Record, error) {
	r, err := tx.GetRecord(s.taskID)
	if err != nil {
		return r, err
	}
	switch {
	case r.UnmetRequires > 0:
		return r, fault.New(fault.CodeNotReady, "%s: a prerequisite became incomplete during verification", r.Task.ID)
	case r.Task.ContractRev != s.contractRev:
		return r, fault.New(fault.CodeConflict, "%s: the task contract changed during verification; verify again", r.Task.ID)
	case (verification.Policy{Regression: r.Task.Verification.Regression}).Digest() != s.regression:
		return r, fault.New(fault.CodeConflict, "%s: the task regression policy changed during verification; verify again", r.Task.ID)
	case r.Task.Archived():
		return r, fault.New(fault.CodeConflict, "%s was archived during verification", r.Task.ID)
	case r.Submission == nil || r.Submission.ID != s.submission:
		return r, fault.New(fault.CodeConflict, "%s was resubmitted during verification", r.Task.ID)
	}
	stored, err := tx.EvidenceForRun(s.runID)
	if err != nil {
		return r, err
	}
	var onFinal []verification.Evidence
	for _, ev := range stored {
		if ev.Revision == final {
			onFinal = append(onFinal, ev)
		}
	}
	if v := verification.Judge(s.policy, onFinal); !v.Passed {
		return r, fault.New(fault.CodeCheckFailed, "stored evidence does not prove revision %s: %s", short(final), v.Summary)
	}
	return r, nil
}
