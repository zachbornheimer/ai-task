package app

import (
	"context"
	"sort"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// Add creates a task. The returned Warnings are atomicity hints; they never
// block creation.
func (e *Engine) Add(ctx context.Context, spec task.Spec) (task.Task, []string, error) {
	if err := spec.Validate(); err != nil {
		return task.Task{}, nil, err
	}
	now := e.now()
	t := task.Task{ProjectID: spec.ProjectID, Description: spec.Description, Outcome: spec.Outcome, Constraints: spec.Constraints, Acceptance: spec.Acceptance, Verification: spec.Verification, CreatedAt: now, UpdatedAt: now}
	// Random IDs: regenerate on the (vanishingly unlikely) collision rather
	// than failing. The UNIQUE constraint is the collision detector.
	for attempt := 0; attempt < 5; attempt++ {
		t.ID = task.NewID()
		err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
			if _, err := tx.GetProject(spec.ProjectID); err != nil {
				return err
			}
			return tx.InsertTask(t)
		})
		if err == nil {
			return t, spec.Warnings(), nil
		}
		if !sqlite.IsUniqueViolation(err) {
			return task.Task{}, nil, err
		}
	}
	return task.Task{}, nil, fault.New(fault.CodeInternal, "could not allocate a unique task id")
}

// Show returns the full read model for a task.
func (e *Engine) Show(ctx context.Context, id task.ID) (TaskView, error) {
	var v TaskView
	now := e.now()
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		r, err := tx.GetRecord(id)
		if err != nil {
			return err
		}
		v, err = e.buildView(tx, r, now)
		return err
	})
	return v, err
}

// ListFilter selects which tasks List returns. An empty Statuses means all.
type ListFilter struct {
	Statuses []task.Status
}

// List returns task summaries of a project, filtered by derived status.
func (e *Engine) List(ctx context.Context, pid project.ID, f ListFilter) ([]TaskSummary, error) {
	now := e.now()
	scope := sqlite.ScopeAll
	// Use the narrowest SQL pre-filter that still contains every requested
	// status; the exact status is derived per row below.
	if len(f.Statuses) > 0 {
		scope = sqlite.ScopeTakeable
		for _, st := range f.Statuses {
			switch {
			case st == task.StatusComplete:
				scope = sqlite.ScopeAll
			case !st.Takeable() && scope == sqlite.ScopeTakeable:
				scope = sqlite.ScopeOpen
			}
		}
	}
	want := map[task.Status]bool{}
	for _, s := range f.Statuses {
		want[s] = true
	}
	var out []TaskSummary
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		if _, err := tx.GetProject(pid); err != nil {
			return err
		}
		records, err := tx.ListRecords(pid, scope, now)
		if err != nil {
			return err
		}
		out = make([]TaskSummary, 0, len(records))
		for _, r := range records {
			s := summarize(r, now)
			if len(want) > 0 && !want[s.Status] {
				continue
			}
			out = append(out, s)
		}
		return nil
	})
	return out, err
}

// Available lists tasks with status "available": fresh work whose
// prerequisites are complete. Interrupted and verification-failed tasks are
// also takeable but are listed by Takeable, so an agent can tell new work
// from recovery work.
func (e *Engine) Available(ctx context.Context, pid project.ID) ([]TaskSummary, error) {
	return e.List(ctx, pid, ListFilter{Statuses: []task.Status{task.StatusAvailable}})
}

// Takeable lists every task a Take could claim right now, in the order
// automatic selection would consider them.
func (e *Engine) Takeable(ctx context.Context, pid project.ID) ([]TaskSummary, error) {
	out, err := e.List(ctx, pid, ListFilter{Statuses: []task.Status{task.StatusInterrupted, task.StatusVerificationFailed, task.StatusAvailable}})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Status.TakePriority() < out[j].Status.TakePriority() })
	return out, nil
}

// History returns a page of a task's log plus its attempts and
// submissions. Tokens are never included.
func (e *Engine) History(ctx context.Context, id task.ID, limit, offset int) (HistoryPage, error) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	now := e.now()
	page := HistoryPage{TaskID: id, Offset: offset}
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		if _, err := tx.GetRecord(id); err != nil {
			return err
		}
		var err error
		if page.Total, err = tx.LogCount(id); err != nil {
			return err
		}
		if page.Entries, err = tx.Logs(id, limit, offset); err != nil {
			return err
		}
		if page.Entries == nil {
			page.Entries = []execution.RecordedLog{}
		}
		attempts, err := tx.Attempts(id)
		if err != nil {
			return err
		}
		for i := range attempts {
			page.Attempts = append(page.Attempts, *attemptView(&attempts[i], now))
		}
		subs, err := tx.Submissions(id)
		if err != nil {
			return err
		}
		for i := range subs {
			page.Submissions = append(page.Submissions, *submissionView(&subs[i]))
		}
		return nil
	})
	return page, err
}
