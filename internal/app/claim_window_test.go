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
)

// A claim records its attempt, builds the worktree, and stores the token in
// that worktree last, so a process killed in between leaves a claim with no
// worktree and no token anybody can release it with. The task must come back
// inside the claim window, not after the whole lease.
func TestClaimWindowFreesATaskWhoseWorktreeWasNeverStored(t *testing.T) {
	f := newGitFixture(t)
	id := f.add("crashable")
	app.SetFaultHook(f.e, func(point string) error {
		if point == "claim:after-attempt" {
			return errors.New("simulated crash between the attempt and its worktree")
		}
		return nil
	})
	if _, err := f.e.Claim(f.ctx, app.ClaimRequest{TaskID: &id}); err == nil {
		t.Fatal("the injected crash must fail the claim")
	}
	app.SetFaultHook(f.e, nil)

	crashed := f.show(string(id))
	if crashed.Status != task.StatusClaimed {
		t.Fatalf("after the crash the task is %s, want %s", crashed.Status, task.StatusClaimed)
	}
	if limit := f.c.Now().Add(execution.ClaimWindow); crashed.Attempt.LeaseExpiresAt.After(limit) {
		t.Fatalf("the unworkable claim holds the task until %s, want no later than the claim window %s",
			crashed.Attempt.LeaseExpiresAt, limit)
	}
	// Inside the window the task still belongs to that claim: a second attempt
	// must not take a worktree out from under the process building it.
	_, err := f.e.Claim(f.ctx, app.ClaimRequest{TaskID: &id})
	wantCode(t, err, fault.CodeConflict)

	f.c.Advance(execution.ClaimWindow + time.Second)
	if got := f.status(string(id)); got != task.StatusInterrupted {
		t.Fatalf("after the claim window the task is %s, want %s", got, task.StatusInterrupted)
	}
	resumed := f.claim(string(id))
	if resumed.AttemptSeq != 2 {
		t.Fatalf("the next claim took attempt %d, want 2", resumed.AttemptSeq)
	}
	if want := f.c.Now().Add(execution.DefaultLease); !resumed.LeaseUntil.Equal(want) {
		t.Fatalf("the next claim's lease runs to %s, want %s", resumed.LeaseUntil, want)
	}
}

// A claim that did store its worktree holds the whole lease, not the window.
func TestStoredWorktreeHoldsTheWholeLease(t *testing.T) {
	f := newGitFixture(t)
	s := f.claim(string(f.add("stored")))
	if want := f.c.Now().Add(execution.DefaultLease); !s.LeaseUntil.Equal(want) {
		t.Fatalf("lease until %s, want %s", s.LeaseUntil, want)
	}
	if v := f.show(string(s.Task.ID)); !v.Attempt.LeaseExpiresAt.Equal(s.LeaseUntil) {
		t.Fatalf("the recorded expiry %s is not the session's %s", v.Attempt.LeaseExpiresAt, s.LeaseUntil)
	}
}

// slowSwitcher builds the worktree like the fixture's own switcher and then
// runs during: the claim's provisioning, aged past the claim window in steps
// that leave room for the claim's own renewals, with another engine reading
// the board — the reconcile that ends an attempt nobody renewed.
type slowSwitcher struct {
	addSwitcher
	during func()
}

func (s slowSwitcher) Ensure(ctx context.Context, repo, branch, base string, create bool) (string, error) {
	path, err := s.addSwitcher.Ensure(ctx, repo, branch, base, create)
	if err == nil {
		s.during()
	}
	return path, err
}

// A claim whose worktree build outlives the claim window keeps the task. The
// window is the time a dead claim may hold a task, so it must not end a claim
// that is still alive and working: provisioning renews it, and another process
// reconciling the board mid-build must still find the attempt live.
func TestProvisioningClaimRenewsItsWindow(t *testing.T) {
	f := newGitFixture(t)
	id := f.add("slow")
	f.stampRegression()
	during := func() {
		for range 6 {
			f.c.Advance(time.Minute)
			time.Sleep(100 * time.Millisecond) // the claim's renewals land here
		}
		f.show(string(id)) // another process reconciles: an unrenewed attempt ends here
	}
	e := f.openWith(app.Config{
		HeartbeatInterval: 5 * time.Millisecond,
		Switch: slowSwitcher{
			addSwitcher: addSwitcher{t: f.t, repo: f.repo, root: filepath.Join(filepath.Dir(f.repo), "ws")},
			during:      during,
		},
	})
	s, err := e.Claim(f.ctx, app.ClaimRequest{TaskID: &id})
	if err != nil {
		t.Fatalf("a claim that provisions for longer than the claim window failed: %v", err)
	}
	if s.Workspace == "" {
		t.Fatalf("the claim returned no worktree: %+v", s)
	}
	if want := f.c.Now().Add(execution.DefaultLease); !s.LeaseUntil.Equal(want) {
		t.Fatalf("the stored claim holds the task until %s, want the whole lease %s", s.LeaseUntil, want)
	}
	if got := f.status(string(id)); got != task.StatusClaimed {
		t.Fatalf("after the slow claim the task is %s, want %s", got, task.StatusClaimed)
	}
}
