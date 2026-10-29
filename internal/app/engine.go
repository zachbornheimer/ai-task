// Package app coordinates the domain packages into use cases and owns the
// transaction boundaries. It does not redefine domain rules: eligibility,
// authority, status, and verdicts come from task, dependency, execution,
// and verification.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/zachbornheimer/ai-task/internal/proof"

	"github.com/zachbornheimer/ai-task/internal/execution"
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
	// WorkspaceRoot holds per-attempt worktrees and verification
	// snapshots; empty means DefaultWorkspaceRoot().
	WorkspaceRoot string
	// LogRoot holds verification run logs; empty means DefaultLogRoot().
	LogRoot string
	// PollInterval bounds how long a waiting Claim sleeps between checks
	// when no in-process notification arrives (other processes may have
	// changed the store). Zero means 2s.
	PollInterval time.Duration
	// HeartbeatInterval is how often a long operation renews its lease.
	// Zero means execution.HeartbeatInterval.
	HeartbeatInterval time.Duration
	// Lease is the lease every claim grants. Zero means
	// execution.DefaultLease (30 minutes); only tests need another value.
	Lease time.Duration
	// Switch creates task worktrees. Nil means WorktrunkSwitcher.
	Switch workspace.Switcher
	// Actor is recorded on newly created tasks (ATASK_ACTOR or USER).
	Actor string
	// Cwd is the working directory of the process that opened the engine,
	// stamped onto newly created tasks.
	Cwd string
	// ProofBudget, when > 0, replaces every run's contract budget (tests).
	ProofBudget time.Duration
	// Sandbox confines checks and actions. Nil reads [sandbox] from the
	// user's config.toml (strict when absent).
	Sandbox *proof.SandboxConfig
}

// Engine is the single entry point for every use case. One Engine per
// process is the norm; it is safe for concurrent use.
type Engine struct {
	store   *sqlite.Store
	now     func() time.Time
	newID   func() task.ID
	wsRoot  string
	logRoot string
	poll    time.Duration
	// heartbeatEvery is the lease renewal interval of long operations.
	heartbeatEvery time.Duration
	lease          time.Duration
	swt            workspace.Switcher
	actor          string
	cwd            string
	proofBudget    time.Duration
	sandbox        proof.SandboxConfig

	mu      sync.Mutex
	changed chan struct{}

	// faults is the crash-injection seam used by tests (see fault()).
	faults func(point string) error
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
	sandbox, err := sandboxConfigOf(cfg)
	if err != nil {
		return nil, err
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
	logRoot := cfg.LogRoot
	if logRoot == "" {
		logRoot = DefaultLogRoot(path)
	}
	poll := cfg.PollInterval
	if poll <= 0 {
		poll = 2 * time.Second
	}
	hb := cfg.HeartbeatInterval
	if hb <= 0 {
		hb = execution.HeartbeatInterval
	}
	lease := cfg.Lease
	if lease <= 0 {
		lease = execution.DefaultLease
	}
	// Storage keeps millisecond precision; truncating here keeps values the
	// caller sees identical to what a later read returns.
	return &Engine{
		store: st, now: func() time.Time { return now().UTC().Truncate(time.Millisecond) },
		newID: newID, wsRoot: wsRoot, logRoot: logRoot, poll: poll, heartbeatEvery: hb, lease: lease, changed: make(chan struct{}),
		swt: cfg.Switch, actor: cfg.Actor, cwd: cfg.Cwd, proofBudget: cfg.ProofBudget, sandbox: sandbox,
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

// DefaultPath returns the OS-appropriate state location, honoring the
// ATASK_DB override. Task state lives outside any source repository.
func DefaultPath() (string, error) {
	if p := os.Getenv("ATASK_DB"); p != "" {
		return p, nil
	}
	base, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "atask", "atask.db"), nil
}

// DefaultWorkspaceRoot places snapshots and quarantined trees beside the database.
func DefaultWorkspaceRoot(dbPath string) string {
	if dbPath == ":memory:" {
		return filepath.Join(os.TempDir(), "atask-workspaces")
	}
	return filepath.Join(filepath.Dir(dbPath), "workspaces")
}

// DefaultLogRoot places verification run logs beside the database.
func DefaultLogRoot(dbPath string) string {
	if dbPath == ":memory:" {
		return filepath.Join(os.TempDir(), "atask-logs")
	}
	return filepath.Join(filepath.Dir(dbPath), "logs")
}

func stateDir() (string, error) {
	home := func() (string, error) {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", fault.WrapInternal(err, "resolve home directory")
		}
		return h, nil
	}
	if runtime.GOOS == "windows" {
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			return base, nil
		}
		h, err := home()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, "AppData", "Local"), nil
	}
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		return base, nil
	}
	h, err := home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".local", "state"), nil
}

// --- repositories (execution substrate) ---

// RepoAt returns the canonical main-worktree root of the repository that
// holds dir, or "" when dir is not inside a Git repository. A linked
// worktree resolves to its main worktree, so every checkout of one
// repository names the same root.
func RepoAt(dir string) (string, error) { return workspace.LocateRoot(dir) }

// BaseRef returns the branch a task branch starts from for repo: the
// repository's default branch. "" when repo is "".
func BaseRef(ctx context.Context, repo string) string {
	if repo == "" {
		return ""
	}
	return (workspace.Manager{Repo: repo}).DefaultBranch(ctx)
}

// InitResult reports what InitRepo did to a directory.
type InitResult struct {
	Repo        string `json:"repo"`
	CreatedRepo bool   `json:"created_repo"`
	// UntrackedFiles lists what the directory already held when init
	// created the repository. init never commits them; Warning says so.
	UntrackedFiles []string `json:"untracked_files,omitempty"`
	Warning        string   `json:"warning,omitempty"`
}

// InitRepo prepares dir as a workable repository: if it is not inside a Git
// repository one is created (with an initial commit), and files that were
// already there stay untracked and are reported. It registers nothing: a
// task records its own repository when it is filed, so no repository has to
// be announced before work can be created against it.
func (e *Engine) InitRepo(ctx context.Context, dir string) (InitResult, error) {
	var res InitResult
	if dir == "" {
		return res, fault.New(fault.CodeInvalidInput, "init needs a directory")
	}
	root, err := workspace.LocateRoot(dir)
	if err != nil {
		return res, fault.Wrap(err, fault.CodeInvalidInput, "resolve repository")
	}
	if root == "" {
		if root, err = workspace.CanonicalRoot(dir); err != nil {
			return res, fault.Wrap(err, fault.CodeInvalidInput, "resolve repository")
		}
	}
	if err := refuseHomeOrAncestor(dir, root); err != nil {
		return res, err
	}
	res.Repo = root
	if res.CreatedRepo, err = workspace.InitRepo(ctx, root); err != nil {
		return res, err
	}
	if res.CreatedRepo {
		if files, err := workspace.UncommittedFiles(ctx, root); err == nil && len(files) > 0 {
			res.UntrackedFiles = files
			res.Warning = fmt.Sprintf("%d existing file(s) are untracked and not part of the repository's commits; commit them before the baseline can pass (at never commits them itself)", len(files))
		}
	}
	return res, nil
}

// refuseHomeOrAncestor rejects a repository root that is the user's home
// directory or one of its ancestors, before any repository is created there.
func refuseHomeOrAncestor(dir, root string) error {
	// An unresolvable home must refuse, not proceed: the guard cannot prove safety.
	home, err := os.UserHomeDir()
	if err != nil {
		return fault.Wrap(err, fault.CodeInvalidInput, "resolve home directory")
	}
	canonicalHome, err := workspace.CanonicalRoot(home)
	if err != nil {
		return fault.Wrap(err, fault.CodeInvalidInput, "resolve home directory")
	}
	rel, err := filepath.Rel(root, canonicalHome)
	if err != nil {
		return fault.Wrap(err, fault.CodeInvalidInput, "compare repository root with home directory")
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	shown, err := filepath.Abs(dir)
	if err != nil {
		shown = dir
	}
	return fault.New(fault.CodeInvalidInput, "refusing to create a repository in %s: choose a project directory", shown)
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
// target branch, attempt budget, retry policy). It
// is never reachable through a session token. Regression check IDs form a
// namespace separate from task checks: a collision with any live task's
// checks is rejected so no task can shadow a project gate.
func (e *Engine) UpdateProject(ctx context.Context, p project.Project) error {
	if err := p.Validate(); err != nil {
		return err
	}
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		return tx.UpdateProject(p, e.now())
	})
	if err == nil {
		e.notify()
	}
	return err
}

// ResolveProject finds the project for a command's planning scope. A
// selector names a project ID or unique name (from --project /
// ATASK_PROJECT); a name matches when project.NameKey is equal
// ("ai-tooling" is "AI Tooling"). Without a selector the answer is the
// general project, created on first use. The working directory is not
// consulted: a project is a planning scope, and the repository a task's
// work runs in is recorded on the task.
func (e *Engine) ResolveProject(ctx context.Context, selector string) (project.Project, error) {
	var out project.Project
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		if selector == project.GeneralName {
			return errUseGeneral
		}
		if selector != "" {
			if id, err := project.ParseID(selector); err == nil {
				p, err := tx.GetProject(id)
				if fault.Is(err, fault.CodeNotFound) {
					return unknownProject(tx, selector)
				}
				if err != nil {
					return err
				}
				out = p
				return nil
			}
			p, ok, err := tx.GetProjectByName(selector)
			if err != nil {
				return err
			}
			if !ok {
				return unknownProject(tx, selector)
			}
			out = p
			return nil
		}
		return errUseGeneral
	})
	if errors.Is(err, errUseGeneral) {
		return e.EnsureGeneral(ctx)
	}
	return out, err
}

// errUseGeneral is an in-process signal from the read lookup to create
// or load the general project. It is never returned to callers.
// It must not be a fault.Error: Error.Is matches every error of the same
// code, so a CodeNotFound sentinel would swallow "no project with id or
// name" and silently select general.
var errUseGeneral = errors.New("use general")

// unknownProject reports an explicit --project selector that matches no
// registered project. It names the selector and lists the registered
// names so a typo is visible. It must carry CodeInvalidInput, never
// CodeNotFound: only the caller's absence of a selector may fall back to
// general, and that path is signaled by errUseGeneral.
func unknownProject(tx *sqlite.Tx, selector string) error {
	ps, err := tx.ListProjects()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(ps))
	for i := range ps {
		names = append(names, ps[i].Name)
	}
	if len(names) == 0 {
		return fault.New(fault.CodeInvalidInput, "unknown project %q; no projects exist yet (atask project add NAME, or file work under general)", selector)
	}
	return fault.New(fault.CodeInvalidInput, "unknown project %q; existing projects: %s", selector, strings.Join(names, ", "))
}

// manager returns the Git workspace manager for a repository. Task IDs are
// globally unique, so one worktree root serves every repository.
func (e *Engine) manager(repo string) workspace.Manager {
	return workspace.Manager{Repo: repo, Root: e.wsRoot, Switch: e.swt}
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
func effectivePolicy(t task.Task, _ project.Project) verification.Policy {
	return verification.Merge(t.Verification, t.Verification.Regression)
}

func sandboxConfigOf(cfg Config) (proof.SandboxConfig, error) {
	if cfg.Sandbox != nil {
		return *cfg.Sandbox, nil
	}
	return loadSandboxConfig()
}
