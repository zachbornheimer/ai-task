package app

import (
	"context"

	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// PruneResult lists what PruneWorkspaces removed.
type PruneResult struct {
	Removed []string `json:"removed"`
}

// PruneWorkspaces removes the worktrees of complete and archived tasks
// (live and quarantined) and the quarantined directories of every other
// task whose current attempt is not running under an unended lease. A
// task that is claimed or being verified keeps everything: a quarantined
// process may still be alive, and an explicit prune is the operator's
// decision to stop caring about it.
func (e *Engine) PruneWorkspaces(ctx context.Context, pid project.ID) (PruneResult, error) {
	var res PruneResult
	proj, err := e.Project(ctx, pid)
	if err != nil {
		return res, err
	}
	now := e.now()
	var records []sqlite.Record
	if err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		var err error
		records, err = tx.ListRecords(pid, sqlite.ScopeAll, now, proj.MaxAttempts)
		return err
	}); err != nil {
		return res, err
	}
	mgr := e.manager(proj)
	for _, r := range records {
		if r.Task.Kind != task.KindTask {
			continue
		}
		st := r.Status(now, proj.MaxAttempts)
		keepLive := st != task.StatusComplete && st != task.StatusArchived
		if keepLive && st.Active() {
			continue
		}
		removed, err := mgr.Prune(ctx, string(r.Task.ID), keepLive)
		res.Removed = append(res.Removed, removed...)
		if err != nil {
			return res, err
		}
	}
	if res.Removed == nil {
		res.Removed = []string{}
	}
	return res, nil
}
