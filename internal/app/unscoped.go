package app

import (
	"context"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
)

const recommendProject = "no project attached; run `atask init` here or pass --project so claim can use a worktree"

// EnsureUnscoped returns the default board (project id ""), creating it
// if needed. Filing work never requires an enrolled repo.
func (e *Engine) EnsureUnscoped(ctx context.Context) (project.Project, error) {
	var out project.Project
	now := e.now()
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		if p, err := tx.GetProject(project.UnscopedID); err == nil {
			out = p
			return nil
		} else if fault.CodeOf(err) != fault.CodeNotFound {
			return err
		}
		p := project.Project{
			ID:          project.UnscopedID,
			Integration: project.IntegrationNone,
			MaxAttempts: project.DefaultMaxAttempts,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := p.Validate(); err != nil {
			return err
		}
		if err := tx.InsertProject(p); err != nil {
			if sqlite.IsUniqueViolation(err) {
				existing, gerr := tx.GetProject(project.UnscopedID)
				out = existing
				return gerr
			}
			return err
		}
		out = p
		return nil
	})
	return out, err
}
