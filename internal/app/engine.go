// Package app coordinates the domain packages into use cases and owns the
// transaction boundaries. It does not redefine domain rules: eligibility,
// authority, and status come from task, dependency, and execution.
package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
)

// Config configures an Engine.
type Config struct {
	// Path of the SQLite database. Empty selects DefaultPath(); ":memory:"
	// opens a private in-memory store.
	Path string
	// Now supplies the clock; nil means time.Now. Tests inject a fake clock
	// to expire leases deterministically.
	Now func() time.Time
}

// Engine is the single entry point for every use case. One Engine per
// process is the norm; it is safe for concurrent use.
type Engine struct {
	store *sqlite.Store
	now   func() time.Time
}

// Open opens the store and applies migrations.
func Open(ctx context.Context, cfg Config) (*Engine, error) {
	path := cfg.Path
	if path == "" {
		var err error
		if path, err = DefaultPath(); err != nil {
			return nil, err
		}
	}
	st, err := sqlite.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	// Storage keeps millisecond precision; truncating here keeps values the
	// caller sees identical to what a later read returns.
	return &Engine{store: st, now: func() time.Time { return now().UTC().Truncate(time.Millisecond) }}, nil
}

// Close releases the store.
func (e *Engine) Close() error { return e.store.Close() }

// Path returns the database path in use.
func (e *Engine) Path() string { return e.store.Path() }

// DefaultPath returns the OS-appropriate state location, honouring the
// TASKS_DB override. Task state lives outside any source repository.
func DefaultPath() (string, error) {
	if p := os.Getenv("TASKS_DB"); p != "" {
		return p, nil
	}
	var base string
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fault.Wrap(err, fault.CodeInternal, "resolve home directory")
		}
		base = filepath.Join(home, "Library", "Application Support")
	case "windows":
		base = os.Getenv("LOCALAPPDATA")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fault.Wrap(err, fault.CodeInternal, "resolve home directory")
			}
			base = filepath.Join(home, "AppData", "Local")
		}
	default:
		base = os.Getenv("XDG_STATE_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fault.Wrap(err, fault.CodeInternal, "resolve home directory")
			}
			base = filepath.Join(home, ".local", "state")
		}
	}
	return filepath.Join(base, "tasks", "tasks.db"), nil
}

// --- projects ---

// InitProject registers a project. Dir may be empty for a project without
// a directory. If dir is inside a Git repository, the repository's main
// worktree root is registered so every worktree resolves to the project.
func (e *Engine) InitProject(ctx context.Context, name, dir string) (project.Project, error) {
	now := e.now()
	p := project.Project{ID: project.NewID(), Name: strings.TrimSpace(name), Integration: project.IntegrationNone, CreatedAt: now, UpdatedAt: now}
	if dir != "" {
		root, err := project.LocateRoot(dir)
		if err != nil {
			return p, fault.Wrap(err, fault.CodeInvalidInput, "resolve project directory")
		}
		if root == "" {
			if root, err = project.CanonicalRoot(dir); err != nil {
				return p, fault.Wrap(err, fault.CodeInvalidInput, "resolve project directory")
			}
		}
		p.RootPath = root
		if p.Name == "" {
			p.Name = filepath.Base(root)
		}
	}
	if err := p.Validate(); err != nil {
		return p, err
	}
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		if p.RootPath != "" {
			if existing, ok, err := tx.GetProjectByRoot(p.RootPath); err != nil {
				return err
			} else if ok {
				return fault.New(fault.CodeInvalidInput, "directory already registered as project %s (%s)", existing.ID, existing.Name)
			}
		}
		return tx.InsertProject(p)
	})
	return p, err
}

// Projects lists registered projects.
func (e *Engine) Projects(ctx context.Context) ([]project.Project, error) {
	var out []project.Project
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		var err error
		out, err = tx.ListProjects()
		return err
	})
	return out, err
}

// Project loads one project.
func (e *Engine) Project(ctx context.Context, id project.ID) (project.Project, error) {
	var out project.Project
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		var err error
		out, err = tx.GetProject(id)
		return err
	})
	return out, err
}

// ResolveProject finds the project for a command. Precedence:
//  1. selector: a project ID or unique name (from --project / TASKS_PROJECT);
//  2. the Git repository containing dir, by registered main-worktree root;
//  3. the longest registered root that contains dir.
func (e *Engine) ResolveProject(ctx context.Context, selector, dir string) (project.Project, error) {
	var out project.Project
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		if selector != "" {
			if id, err := project.ParseID(selector); err == nil {
				p, err := tx.GetProject(id)
				out = p
				return err
			}
			p, ok, err := tx.GetProjectByName(selector)
			if err != nil {
				return err
			}
			if !ok {
				return fault.New(fault.CodeNotFound, "no project with id or name %q", selector)
			}
			out = p
			return nil
		}
		if dir == "" {
			return fault.New(fault.CodeNoProject, "no project selected; pass --project or set TASKS_PROJECT")
		}
		root, err := project.LocateRoot(dir)
		if err != nil {
			return fault.Wrap(err, fault.CodeInternal, "locate repository root")
		}
		if root != "" {
			if p, ok, err := tx.GetProjectByRoot(root); err != nil {
				return err
			} else if ok {
				out = p
				return nil
			}
		}
		canon, err := project.CanonicalRoot(dir)
		if err != nil {
			return fault.Wrap(err, fault.CodeInternal, "canonicalise directory")
		}
		roots, err := tx.ProjectRoots()
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(roots))
		for r := range roots {
			keys = append(keys, r)
		}
		if best := project.MatchRoot(canon, keys); best != "" {
			p, err := tx.GetProject(roots[best])
			out = p
			return err
		}
		return fault.New(fault.CodeNoProject, "no project registered for %s; run `tasks init` here or pass --project", canon)
	})
	return out, err
}

// UpdateProject rewrites a project's mutable configuration (name, root,
// integration policy, regression checks). It is a separate operation from
// any execution session: an agent's token cannot weaken verification.
func (e *Engine) UpdateProject(ctx context.Context, p project.Project) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return e.store.Write(ctx, func(tx *sqlite.Tx) error {
		return tx.UpdateProject(p, e.now())
	})
}
