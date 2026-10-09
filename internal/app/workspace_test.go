package app_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// One task whose worktree cannot be prepared must not starve the queue:
// it is marked needs_attention with the reason, automatic claims skip it,
// list/summary agree, an explicit claim retries it, and a planner reset
// clears it.
func TestUnusableWorktreeDoesNotBlockTheQueue(t *testing.T) {
	f := newGitFixture(t)
	old := f.add("old")
	f.c.Advance(time.Second) // oldest-first selection must try `old` before `young`
	young := f.add("young")
	// Break the oldest task's worktree: an agent left it on another branch.
	s := f.claim(string(old))
	if _, err := f.e.Release(f.ctx, s.Token, app.ReleaseOptions{}); err != nil {
		t.Fatal(err)
	}
	f.git(s.Workspace, "checkout", "-q", "-b", "somewhere-else")

	// Automatic claim (waiting) gets the younger task at once.
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	got, err := f.e.Claim(ctx, app.ClaimRequest{ProjectID: f.proj.ID, Wait: true})
	if err != nil || got.Task.ID != young {
		t.Fatalf("waiting claim: %+v %v", got.Task.ID, err)
	}
	// The broken task says why, and every view agrees it is not claimable.
	v := f.show(string(old))
	if v.Status != task.StatusNeedsAttention || !strings.Contains(v.AttentionReason, "workspace unusable") || v.Failures != 0 {
		t.Fatalf("broken task: %s %q failures=%d", v.Status, v.AttentionReason, v.Failures)
	}
	snap, _ := f.e.List(f.ctx, app.ListQuery{ProjectID: f.proj.ID, Filter: app.FilterReady})
	for _, tv := range snap.Tasks {
		if tv.ID == old {
			t.Fatal("broken task listed as ready")
		}
	}
	sum, _ := f.e.Summary(f.ctx, f.proj.ID)
	if sum.Claimable != 0 || sum.Stalled || sum.Active != 1 {
		t.Fatalf("summary: %+v", sum)
	}
	_, err = f.e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID})
	wantCode(t, err, fault.CodeNoAvailableTask)
	// With the only other work done, a waiting claim reports a stall with
	// the reason instead of hot-looping on the broken task.
	f.complete(got)
	_, err = f.e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID, Wait: true})
	wantCode(t, err, fault.CodeStalled)
	if fe, _ := err.(*fault.Error); fe == nil || !strings.Contains(strings.Join(fe.Details.(app.Summary).Reasons, " "), "unusable workspace") {
		t.Fatalf("stall reasons: %v", err)
	}
	// An explicit claim retries; it fails again while the worktree is still
	// broken, with the exact problem, and nothing is counted.
	_, err = f.e.Claim(f.ctx, app.ClaimRequest{TaskID: &old})
	wantCode(t, err, fault.CodeWorkspaceUnavailable)
	if v := f.show(string(old)); v.Failures != 0 || v.Status != task.StatusNeedsAttention {
		t.Fatalf("%+v", v)
	}
	// Repair the worktree; the explicit claim now succeeds and clears the mark.
	f.git(s.Workspace, "checkout", "-q", "at/"+string(old))
	s2, err := f.e.Claim(f.ctx, app.ClaimRequest{TaskID: &old})
	if err != nil {
		t.Fatal(err)
	}
	if v := f.show(string(old)); v.Status != task.StatusClaimed || v.AttentionReason != "" {
		t.Fatalf("%+v", v)
	}
	f.complete(s2)

	// A planner reset also clears the mark (the alternative repair path).
	z := f.add("z")
	sz := f.claim(string(z))
	f.e.Release(f.ctx, sz.Token, app.ReleaseOptions{})
	f.git(sz.Workspace, "checkout", "-q", "--detach")
	_, err = f.e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID})
	wantCode(t, err, fault.CodeWorkspaceUnavailable)
	if f.status(string(z)) != task.StatusNeedsAttention {
		t.Fatal("not marked")
	}
	f.git(sz.Workspace, "checkout", "-q", "at/"+string(z))
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Planner: true, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(z), ResetAttempts: true}}}); err != nil {
		t.Fatal(err)
	}
	if f.status(string(z)) != task.StatusReady {
		t.Fatalf("reset did not clear: %s", f.status(string(z)))
	}
}

// Quarantined directories are removed when the task completes; `prune`
// removes complete and archived tasks' worktrees and idle tasks'
// quarantines, never a live claim's worktree.
func TestPruneWorkspaces(t *testing.T) {
	f := newGitFixture(t)
	a := f.add("a")
	sA := f.claim(string(a))
	f.commit(sA.Workspace, "a.txt", "a")
	f.c.Advance(execution.DefaultLease + time.Minute)
	sB := f.claim(string(a)) // quarantines attempt 1
	if _, err := os.Stat(sB.QuarantinedWorkspace); err != nil {
		t.Fatal("no quarantine")
	}
	b := f.add("b")
	sb := f.claim(string(b))
	f.complete(sB)
	if _, err := os.Stat(sB.QuarantinedWorkspace); !os.IsNotExist(err) {
		t.Fatal("completion did not remove the quarantined directory")
	}
	res, err := f.e.PruneWorkspaces(f.ctx, f.proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sB.Workspace); !os.IsNotExist(err) {
		t.Fatalf("complete task's worktree kept: %v", res.Removed)
	}
	if _, err := os.Stat(sb.Workspace); err != nil {
		t.Fatal("live claim's worktree pruned")
	}
	if strings.TrimSpace(f.git(f.repo, "branch", "--list", "at/"+string(a))) == "" {
		t.Fatal("branch deleted by prune")
	}
}
