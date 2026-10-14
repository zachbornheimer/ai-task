package app

import (
	"context"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
)

// AddProject creates a rootless project (no repository, no target branch),
// stored exactly like general. A name that already exists returns that
// project unchanged. Like InitProject it is trusted configuration and is
// not part of the public at.Engine.
func (e *Engine) AddProject(ctx context.Context, name string) (project.Project, error) {
	name = strings.TrimSpace(name)
	if name == project.GeneralName {
		return e.EnsureGeneral(ctx)
	}
	now := e.now()
	p := project.Project{ID: project.NewID(), Name: name, MaxAttempts: project.DefaultMaxAttempts, CreatedAt: now, UpdatedAt: now}
	if err := p.Validate(); err != nil {
		return p, err
	}
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		if existing, ok, err := tx.GetProjectByName(name); err != nil {
			return err
		} else if ok {
			p = existing
			return nil
		}
		return tx.InsertProject(p)
	})
	return p, err
}
