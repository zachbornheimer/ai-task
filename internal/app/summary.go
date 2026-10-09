package app

import (
	"context"

	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// Summary is the project-level view a driver loop needs to decide whether
// to keep taking work, wait, or stop.
type Summary struct {
	ProjectID project.ID          `json:"project_id"`
	Total     int                 `json:"total"`
	Open      int                 `json:"open"`
	Counts    map[task.Status]int `json:"counts"`
	// Takeable counts tasks automatic take could claim now (manual tasks
	// excluded); ManualPending counts takeable tasks that need a human.
	Takeable      int `json:"takeable"`
	ManualPending int `json:"manual_pending"`
	// Active counts tasks whose outcome is still being produced or judged:
	// in_progress, awaiting_verification, awaiting_integration.
	Active int `json:"active"`
	// Done: every task is complete.
	Done bool `json:"done"`
	// Stuck: open tasks remain, nothing is takeable by a machine, and
	// nothing is active. Progress needs a human: a manual task, a new
	// prerequisite, a dependency change, or a verification fix.
	Stuck bool `json:"stuck"`
}

// Summary computes the project status in one read transaction.
func (e *Engine) Summary(ctx context.Context, pid project.ID) (Summary, error) {
	now := e.now()
	s := Summary{ProjectID: pid, Counts: map[task.Status]int{}}
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		if _, err := tx.GetProject(pid); err != nil {
			return err
		}
		records, err := tx.ListRecords(pid, sqlite.ScopeAll, now)
		if err != nil {
			return err
		}
		for _, r := range records {
			st := r.Status(now)
			s.Total++
			s.Counts[st]++
			if st != task.StatusComplete {
				s.Open++
			}
			switch {
			case st.Takeable() && r.Task.Manual:
				s.ManualPending++
			case st.Takeable():
				s.Takeable++
			case st == task.StatusInProgress, st == task.StatusAwaitingVerification, st == task.StatusAwaitingIntegration:
				s.Active++
			}
		}
		s.Done = s.Open == 0
		s.Stuck = s.Open > 0 && s.Takeable == 0 && s.Active == 0
		return nil
	})
	return s, err
}
