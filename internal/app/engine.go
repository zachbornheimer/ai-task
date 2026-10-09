// Package app coordinates the domain packages into use cases and owns the
// transaction boundaries. It does not redefine domain rules: eligibility,
// authority, status, and verdicts come from task, dependency, execution,
// and verification.
package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

// Config configures an Engine.
type Config struct {
	// Path of the SQLite database. Empty selects DefaultPath(); ":memory:"
	// opens a private in-memory store.
	Path string
	// Now supplies the clock; nil means time.Now. Tests inject a fake clock
	// to expire leases deterministically.
	Now func() time.Time
	// NewTaskID supplies task IDs; nil means random. Tests inject a
	// deterministic generator for golden output.
	NewTaskID func() task.ID
	// WorkspaceRoot holds per-attempt worktrees for projects that do not
	// set their own; empty means DefaultWorkspaceRoot().
	WorkspaceRoot string
	// PollInterval bounds how long a waiting Claim sleeps between checks
	// when no in-process notification arrives (other processes may have
	// changed the store). Zero means 2s.
	PollInterval time.Duration
}

// Engine is the single entry point for every use case. One Engine per
// process is the norm; it is safe for concurrent use.
type Engine struct {
	store  *sqlite.Store
	now    func() time.Time
	newID  func() task.ID
	wsRoot string
	poll   time.Duration

	mu      sync.Mutex
	changed chan struct{}
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
	newID := cfg.NewTaskID
	if newID == nil {
		newID = task.NewID
	}
	wsRoot := cfg.WorkspaceRoot
	if wsRoot == "" {
		wsRoot = DefaultWorkspaceRoot(path)
	}
	poll := cfg.PollInterval
	if poll <= 0 {
		poll = 2 * time.Second
	}
	// Storage keeps millisecond precision; truncating here keeps values the
	// caller sees identical to what a later read returns.
	return &Engine{
		store: st, now: func() time.Time { return now().UTC().Truncate(time.Millisecond) },
		newID: newID, wsRoot: wsRoot, poll: poll, changed: make(chan struct{}),
	}, nil
}

// Close releases the store.
func (e *Engine) Close() error { return e.store.Close() }

// Path returns the database path in use.
func (e *Engine) Path() string { return e.store.Path() }

// notify wakes in-process waiters after a mutation.
func (e *Engine) notify() {
	e.mu.Lock()
	close(e.changed)
	e.changed = make(chan struct{})
	e.mu.Unlock()
}

// changes returns a channel closed on the next in-process mutation.
func (e *Engine) changes() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.changed
}

// DefaultPath returns the OS-appropriate state location, honouring the
// AT_DB override. Task state lives outside any source repository.
func DefaultPath() (string, error) {
	if p := os.Getenv("AT_DB"); p != "" {
		return p, nil
	}
	base, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "at", "at.db"), nil
}

// DefaultWorkspaceRoot places worktrees beside the database.
func DefaultWorkspaceRoot(dbPath string) string {
	if dbPath == ":memory:" {
		return filepath.Join(os.TempDir(), "at-workspaces")
	}
	return filepath.Join(filepath.Dir(dbPath), "workspaces")
}

func stateDir() (string, error) {
	home := func() (string, error) {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", fault.Wrap(err, fault.CodeInternal, "resolve home directory")
		}
		return h, nil
	}
	switch runtime.GOOS {
	case "darwin":
		h, err := home()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, "Library", "Application Support"), nil
	case "windows":
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			return base, nil
		}
		h, err := home()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, "AppData", "Local"), nil
	default:
		if base := os.Getenv("XDG_STATE_HOME"); base != "" {
			return base, nil
		}
		h, err := home()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, ".local", "state"), nil
	}
}

// --- projects (bootstrap and trusted configuration) ---

// InitResult reports what InitProject did to the directory.
type InitResult struct {
	Project     project.Project `json:"project"`
	CreatedRepo bool            `json:"created_repo"`
}

// InitProject registers a Git project. Every project is a Git project: if
// dir is not a repository one is created (with an initial commit); if it is
// inside a repository, the main worktree root is registered. The current
// branch becomes the target branch and integration policy is "promote".
// Nothing is written into the repository's hooks or working tree.
func (e *Engine) InitProject(ctx context.Context, name, dir string) (project.Project, error) {
	res, err := e.InitProjectResult(ctx, name, dir)
	return res.Project, err
}

// InitProjectResult is InitProject with details about the directory.
func (e *Engine) InitProjectResult(ctx context.Context, name, dir string) (InitResult, error) {
	var res InitResult
	if dir == "" {
		return res, fault.New(fault.CodeInvalidInput, "a project needs a directory")
	}
	now := e.now()
	p := project.Project{ID: project.NewID(), Name: strings.TrimSpace(name), Integration: project.IntegrationPromote, MaxAttempts: project.DefaultMaxAttempts, CreatedAt: now, UpdatedAt: now}
	root, err := project.LocateRoot(dir)
	if err != nil {
		return res, fault.Wrap(err, fault.CodeInvalidInput, "resolve project directory")
	}
	if root == "" {
		if root, err = project.CanonicalRoot(dir); err != nil {
			return res, fault.Wrap(err, fault.CodeInvalidInput, "resolve project directory")
		}
	}
	if res.CreatedRepo, err = workspace.InitRepo(ctx, root); err != nil {
		return res, err
	}
	p.RootPath = root
	if p.Name == "" {
		p.Name = filepath.Base(root)
	}
	p.TargetBranch = (workspace.Manager{Repo: root}).DefaultBranch(ctx)
	if err := p.Validate(); err != nil {
		return res, err
	}
	err = e.store.Write(ctx, func(tx *sqlite.Tx) error {
		if p.RootPath != "" {
			if existing, ok, err := tx.GetProjectByRoot(p.RootPath); err != nil {
				return err
			} else if ok {
				return fault.New(fault.CodeInvalidInput, "directory already registered as project %s (%s)", existing.ID, existing.Name)
			}
		}
		return tx.InsertProject(p)
	})
	res.Project = p
	return res, err
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

// UpdateProject rewrites a project's trusted configuration (name,
// integration policy, target branch, regression checks, retry policy). It
// is never reachable through a session token. Regression check IDs form a
// namespace separate from task checks: a collision with any live task's
// checks is rejected so no task can shadow a project gate.
func (e *Engine) UpdateProject(ctx context.Context, p project.Project) error {
	if err := p.Validate(); err != nil {
		return err
	}
	// An empty list means "not configured yet" (no claim is possible); a
	// non-empty list must be a gate.
	if len(p.Regression) > 0 {
		if err := verification.RequireGate(p.Regression, "project regression checks"); err != nil {
			return err
		}
	}
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		used, err := tx.TaskCheckIDs(p.ID)
		if err != nil {
			return err
		}
		for _, c := range p.Regression {
			if tid, ok := used[c.ID]; ok {
				return fault.New(fault.CodeInvalidInput, "regression check id %q collides with a task check of %s; task and regression checks are separate namespaces", c.ID, tid)
			}
		}
		return tx.UpdateProject(p, e.now())
	})
	if err == nil {
		e.notify()
	}
	return err
}

// ResolveProject finds the project for a command. Precedence:
//  1. selector: a project ID or unique name (from --project / AT_PROJECT);
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
			return fault.New(fault.CodeNoProject, "no project selected; pass --project or set AT_PROJECT")
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
		return fault.New(fault.CodeNoProject, "no project registered for %s; run `at init` here or pass --project", canon)
	})
	return out, err
}

// manager returns the Git workspace manager for a project.
func (e *Engine) manager(p project.Project) workspace.Manager {
	root := p.WorkspaceRoot
	if root == "" {
		root = filepath.Join(e.wsRoot, string(p.ID))
	}
	return workspace.Manager{Repo: p.RootPath, Root: root}
}

// reconcile closes runs and jobs whose owners' leases expired so that no
// phantom "verifying" state survives a crash. It is cheap and idempotent.
func (e *Engine) reconcile(ctx context.Context) error {
	now := e.now()
	var n int64
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		var err error
		n, err = tx.ReconcileRuns(now)
		return err
	})
	if err == nil && n > 0 {
		e.notify()
	}
	return err
}

// effectivePolicy merges a task's checks with the project regression
// checks; the two namespaces never overlap by construction.
func effectivePolicy(t task.Task, p project.Project) verification.Policy {
	return verification.Merge(t.Verification, p.Regression)
}
