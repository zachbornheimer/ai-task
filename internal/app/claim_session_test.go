package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

// engineAt opens another engine on the fixture's database as a process whose
// working directory is cwd, and whose worktrees sw builds. A zero lease means
// the default claim budget.
func (f *fixture) engineAt(cwd string, budget time.Duration, sw workspace.Switcher) *app.Engine {
	f.t.Helper()
	e, err := app.Open(f.ctx, app.Config{Path: f.path, Now: f.c.Now, PollInterval: 50 * time.Millisecond, Cwd: cwd, ClaimBudget: budget, Switch: sw})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = e.Close() })
	return e
}

func (f *fixture) worktrees() addSwitcher {
	return addSwitcher{t: f.t, repo: f.repo, root: f.t.TempDir()}
}

// observingSwitcher runs during while the claim is building its worktree, the
// stretch in which a real `wt switch` spends its time.
type observingSwitcher struct {
	addSwitcher
	during func()
}

func (o observingSwitcher) Ensure(ctx context.Context, repo, branch, base string, create bool) (string, error) {
	o.during()
	return o.addSwitcher.Ensure(ctx, repo, branch, base, create)
}

// stuckSwitcher never finishes: a `wt switch` blocked behind a git hook.
type stuckSwitcher struct{}

func (stuckSwitcher) Ensure(ctx context.Context, _, _, _ string, _ bool) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// A claim made from the repository root leaves a session that `verify`
// resolves from that same directory without a token.
func TestClaimLeavesItsSessionWhereVerifyFindsIt(t *testing.T) {
	f := newGitFixture(t)
	e := f.engineAt(f.repo, 0, f.worktrees())
	id := f.add("resolvable")
	f.stampRegression()
	s, err := e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID, TaskID: &id})
	if err != nil {
		t.Fatal(err)
	}

	tok, err := e.AmbientToken(f.ctx)
	if err != nil || tok != s.Token {
		t.Fatalf("the claiming directory resolves token %q (%v), want the claim's %q", tok, err, s.Token)
	}
	f.commit(s.Workspace, "resolvable.txt", "work")
	res, err := e.Verify(f.ctx, tok, verification.ModeComplete)
	if err != nil || !res.Completed {
		t.Fatalf("verify with the directory's session: %+v %v", res, err)
	}
}

// The session exists before the worktree does: a claim that is still waiting
// on `wt switch` (or killed there) has already left its token where the
// directory finds it.
func TestClaimStoresItsSessionBeforeBuildingTheWorktree(t *testing.T) {
	f := newGitFixture(t)
	other := f.engineAt(f.repo, 0, f.worktrees())
	var seen struct {
		token execution.Token
		err   error
	}
	e := f.engineAt(f.repo, 0, observingSwitcher{addSwitcher: f.worktrees(), during: func() {
		seen.token, seen.err = other.AmbientToken(f.ctx)
	}})
	id := f.add("early")
	f.stampRegression()
	s, err := e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID, TaskID: &id})
	if err != nil {
		t.Fatal(err)
	}
	if seen.err != nil || seen.token == "" || seen.token != s.Token {
		t.Fatalf("while the worktree was being built the directory resolved %q (%v), want the claim's token", seen.token, seen.err)
	}
}

// A claim whose worktree cannot be built returns inside the claim window and
// gives the task back, instead of blocking with the task held.
func TestClaimThatCannotBuildItsWorktreeReturnsAndReleasesTheTask(t *testing.T) {
	f := newGitFixture(t)
	ctx, cancel := context.WithCancel(f.ctx)
	t.Cleanup(cancel)
	e := f.engineAt(f.repo, 300*time.Millisecond, stuckSwitcher{})
	id := f.add("stuck")
	f.stampRegression()

	done := make(chan error, 1)
	go func() {
		_, err := e.Claim(ctx, app.ClaimRequest{ProjectID: f.proj.ID, TaskID: &id})
		done <- err
	}()
	select {
	case err := <-done:
		wantCode(t, err, fault.CodeNotReady)
	case <-time.After(10 * time.Second):
		t.Fatal("the claim was still blocked after 10s although its claim budget is 300ms")
	}
	if st := f.status(string(id)); !st.Claimable() {
		t.Fatalf("after the failed claim the task is %s, want it claimable again", st)
	}
	if got := sessionFiles(t, filepath.Dir(f.path)); len(got) != 0 {
		t.Fatalf("the failed claim left %d stored session(s)", len(got))
	}
}

// A claim killed after its attempt was recorded is taken over by claiming the
// task again from the same directory: the new attempt supersedes the killed
// one at once, instead of the task being refused until the claim window ends.
func TestKilledClaimIsTakenOverFromItsDirectory(t *testing.T) {
	f := newGitFixture(t)
	e := f.engineAt(f.repo, 0, f.worktrees())
	id := f.add("resumable")
	f.stampRegression()
	app.SetFaultHook(e, func(point string) error {
		if point == "claim:after-attempt" {
			return errors.New("simulated kill between the attempt and its worktree")
		}
		return nil
	})
	if _, err := e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID, TaskID: &id}); err == nil {
		t.Fatal("the injected kill must fail the claim")
	}
	app.SetFaultHook(e, nil)

	s, err := e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID, TaskID: &id})
	if err != nil {
		t.Fatalf("claiming again from the same directory: %v", err)
	}
	if s.AttemptSeq != 2 || s.Workspace == "" {
		t.Fatalf("retaken as attempt %d with workspace %q, want attempt 2 (superseding the killed attempt 1) with a workspace", s.AttemptSeq, s.Workspace)
	}
	if tok, err := e.AmbientToken(f.ctx); err != nil || tok != s.Token {
		t.Fatalf("directory token %q (%v), want %q", tok, err, s.Token)
	}
	if st := f.status(string(id)); st != task.StatusClaimed {
		t.Fatalf("task is %s, want %s", st, task.StatusClaimed)
	}
}
