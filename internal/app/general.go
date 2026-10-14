package app

import (
	"context"

	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
)

const recommendProject = "no project attached; run `atask init` here or pass --project so claim can use a worktree"

// EnsureGeneral returns the default project (named general), creating it
// on first use. Filing work never requires an enrolled repo.
func (e *Engine) EnsureGeneral(ctx context.Context) (project.Project, error) {
	var out project.Project
	now := e.now()
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		if p, ok, err := tx.GetProjectByName(project.GeneralName); err != nil {
			return err
		} else if ok {
			out = p
			return nil
		}
		p := project.Project{
			ID:          project.NewID(),
			Name:        project.GeneralName,
			MaxAttempts: project.DefaultMaxAttempts,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := p.Validate(); err != nil {
			return err
		}
		if err := tx.InsertProject(p); err != nil {
			return err
		}
		out = p
		return nil
	})
	return out, err
}
