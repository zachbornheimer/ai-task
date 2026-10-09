package app

import (
	"context"
	"fmt"

	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// SetTaskPolicy replaces a task's verification policy. It is deliberately
// not reachable through a session token: changing what must be proven is a
// separate act from implementing.
//
// If the task's newest submission was judged (or is pending) under a
// different effective policy, that run is marked stale, the completion fact
// is withdrawn, and the task returns to awaiting_verification until
// `tasks verify` judges it under the new policy. Evidence for checks whose
// content did not change is reused at the same revision.
func (e *Engine) SetTaskPolicy(ctx context.Context, id task.ID, policy verification.Policy) (TaskView, error) {
	if err := policy.Validate(); err != nil {
		return TaskView{}, err
	}
	now := e.now()
	var view TaskView
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		r, err := tx.GetRecord(id)
		if err != nil {
			return err
		}
		if err := tx.UpdateTaskPolicy(id, policy, now); err != nil {
			return err
		}
		proj, err := tx.GetProject(r.Task.ProjectID)
		if err != nil {
			return err
		}
		effective := verification.Merge(policy, proj.Regression)
		if sub := r.Submission; sub != nil && sub.Run != nil && sub.Run.Status != sqlite.RunStale && sub.Run.PolicyDigest != effective.Digest() {
			summary := fmt.Sprintf("task policy changed (%s -> %s); run %d no longer applies", shortDigest(sub.Run.PolicyDigest), shortDigest(effective.Digest()), sub.Run.ID)
			if _, err := tx.InsertRun(sub.ID, sqlite.RunStale, effective, "", summary, now); err != nil {
				return err
			}
			if r.CompletedAt != nil {
				if err := tx.ClearComplete(id, now); err != nil {
					return err
				}
			}
		}
		r2, err := tx.GetRecord(id)
		if err != nil {
			return err
		}
		view, err = e.buildView(tx, r2, now)
		return err
	})
	return view, err
}

func shortDigest(d string) string {
	if len(d) > 19 {
		return d[:19]
	}
	return d
}
