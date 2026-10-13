package app_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/testgit"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// clock is a settable clock shared by every engine in a test.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)} }
func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fixture struct {
	t    *testing.T
	ctx  context.Context
	e    *app.Engine
	c    *clock
	path string
	proj project.Project
	repo string
}

// newFixture is newGitFixture: every project is a Git project.
func newFixture(t *testing.T) *fixture { t.Helper(); return newGitFixture(t) }

// okChecks is the minimal task check set every task must carry.
func okChecks() []verification.CheckSpec { return []verification.CheckSpec{check("u", "true", true)} }

// newGitFixture creates a project whose root is a Git repository with one
// commit on main and a passing regression check.
func newGitFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	f := &fixture{t: t, ctx: context.Background(), c: newClock(), path: filepath.Join(t.TempDir(), "at.db"), repo: filepath.Join(t.TempDir(), "repo")}
	f.e = f.open()
	os.MkdirAll(f.repo, 0o755)
	f.git(f.repo, "init", "-q", "-b", "main")
	f.git(f.repo, "config", "user.name", "t")
	f.git(f.repo, "config", "user.email", "t@t")
	f.commit(f.repo, "f.txt", "one")
	var err error
	f.proj, err = f.e.InitProject(f.ctx, "gitproj", f.repo)
	if err != nil {
		t.Fatal(err)
	}
	f.setRegression(check("regress", "true", true))
	return f
}

// open opens another engine on the same database, simulating a separate
// process (separate connection pool, same file).
func (f *fixture) open() *app.Engine {
	e, err := app.Open(f.ctx, app.Config{Path: f.path, Now: f.c.Now, PollInterval: 50 * time.Millisecond, Switch: addSwitcher{t: f.t, repo: f.repo, root: filepath.Join(filepath.Dir(f.repo), "ws")}})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { e.Close() })
	return e
}

func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	return testgit.Run(f.t, dir, args...)
}

// commit writes a file and commits in dir, returning the new HEAD.
func (f *fixture) commit(dir, file, content string) string {
	f.t.Helper()
	os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644)
	f.git(dir, "add", file)
	f.git(dir, "commit", "-q", "-m", content)
	return f.git(dir, "rev-parse", "HEAD")
}

func (f *fixture) setRegression(checks ...verification.CheckSpec) {
	f.t.Helper()
	p, err := f.e.Project(f.ctx, f.proj.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	p.Regression = checks
	if err := f.e.UpdateProject(f.ctx, p); err != nil {
		f.t.Fatal(err)
	}
	f.proj = p
}

func sh(script string) []string { return []string{"sh", "-c", script} }

func check(id, script string, required bool) verification.CheckSpec {
	return verification.CheckSpec{ID: id, Command: sh(script), Required: required, Timeout: 20 * time.Second}
}

// apply applies one or more changes and fails the test on error.
func (f *fixture) apply(ops ...plan.Change) plan.Result {
	f.t.Helper()
	res, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: ops})
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

// add creates a task with a trivially passing check and returns its ID.
func (f *fixture) add(key string, requires ...plan.Ref) task.ID {
	f.t.Helper()
	res := f.apply(plan.AddTask{Key: key, Title: key, Requires: requires, TaskChecks: []verification.CheckSpec{check("unit-"+key, "true", true)}})
	return res.Created[key]
}

// addWith creates a task with the given check script.
func (f *fixture) addWith(key, script string, requires ...plan.Ref) task.ID {
	f.t.Helper()
	res := f.apply(plan.AddTask{Key: key, Title: key, Requires: requires, TaskChecks: []verification.CheckSpec{check("unit-"+key, script, true)}})
	return res.Created[key]
}

func (f *fixture) show(ref string) app.TaskView {
	f.t.Helper()
	v, err := f.e.Show(f.ctx, f.proj.ID, ref, false)
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

func (f *fixture) status(ref string) task.Status { return f.show(ref).Status }

func (f *fixture) claim(ref string) app.Session {
	f.t.Helper()
	var id *task.ID
	if ref != "" {
		v := f.show(ref)
		id = &v.ID
	}
	s, err := f.e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID, TaskID: id})
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

// complete claims (if needed), commits a file in the workspace for Git
// projects, and runs verify complete; it fails the test unless the task
// completes.
func (f *fixture) complete(s app.Session) app.VerifyResult {
	f.t.Helper()
	if s.Branch != "" {
		f.commit(s.Workspace, string(s.Task.ID)+".txt", "work for "+string(s.Task.ID))
	}
	res, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	if err != nil || !res.Completed {
		f.t.Fatalf("complete %s: %+v %v", s.Task.ID, res, err)
	}
	return res
}

func wantCode(t *testing.T, err error, code fault.Code) {
	t.Helper()
	if fault.CodeOf(err) != code {
		t.Fatalf("got error %v (code %q), want %q", err, fault.CodeOf(err), code)
	}
}

func strptr(s string) *string { return &s }

type addSwitcher struct {
	t    testing.TB
	repo string
	root string
}

func (a addSwitcher) Ensure(ctx context.Context, _, branch, base string, create bool) (string, error) {
	if err := os.MkdirAll(a.root, 0o755); err != nil {
		return "", err
	}
	root := a.root
	if resolved, err := filepath.EvalSymlinks(a.root); err == nil {
		root = resolved
	}
	path := filepath.Join(root, strings.ReplaceAll(branch, "/", "-"))
	if create {
		cmd := testgit.CommandContext(a.t, ctx, a.repo, "branch", branch, base)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("branch: %w\n%s", err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return path, nil
	}
	cmd := testgit.CommandContext(a.t, ctx, a.repo, "worktree", "add", path, branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("worktree add: %w\n%s", err, out)
	}
	return path, nil
}
