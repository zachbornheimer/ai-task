package app_test

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func groupDetails(t *testing.T, err error) any {
	t.Helper()
	fe, ok := errors.AsType[*fault.Error](err)
	if !ok {
		t.Fatalf("not a fault error: %v", err)
	}
	return fe.Details
}

func claimGroup(f *fixture, g task.ID, wait bool) (app.Session, error) {
	f.t.Helper()
	f.stampRegression()
	return f.e.Claim(f.ctx, app.ClaimRequest{GroupID: g, Wait: wait})
}

// TestClaimGroup: a group claim takes the oldest claimable task beneath the
// group, at any depth, never a task outside it however old; nothing
// claimable yet answers NOT_READY with what is in flight; once every
// member is complete, the waiting claim runs the proofs of the group and
// of the group nested in it, and answers DONE.
func TestClaimGroup(t *testing.T) {
	f := newGitFixture(t)
	// Creation order decides "oldest": the clock moves between plan steps.
	created := map[string]task.ID{}
	for _, op := range []plan.Change{
		plan.AddTask{Key: "outside", Title: "outside", TaskChecks: okChecks()},
		plan.AddGroup{Key: "g", Title: "G", TaskChecks: []verification.CheckSpec{check("outer", "test -d .", true)}},
		plan.AddTask{Key: "a", Title: "a", Parent: "g", TaskChecks: okChecks()},
		plan.AddGroup{Key: "n", Title: "N", Parent: "g", TaskChecks: []verification.CheckSpec{check("inner", "test -d .", true)}},
		plan.AddTask{Key: "b", Title: "b", Parent: "n", TaskChecks: okChecks()},
	} {
		maps.Copy(created, f.apply(op).Created)
		f.c.Advance(time.Second)
	}
	g, n, a, b, outside := created["g"], created["n"], created["a"], created["b"], created["outside"]

	s1, err := claimGroup(f, g, false)
	if err != nil || s1.Task.ID != a {
		t.Fatalf("first group claim = %v %v, want %s (the outside task is older but not a member)", s1.Task.ID, err, a)
	}
	s2, err := claimGroup(f, g, false)
	if err != nil || s2.Task.ID != b {
		t.Fatalf("second group claim = %v %v, want the nested member %s", s2.Task.ID, err, b)
	}
	_, err = claimGroup(f, g, false)
	wantCode(t, err, fault.CodeNotReady)
	if d, ok := groupDetails(t, err).(app.GroupNotReady); !ok || d.Group != g || d.GroupProofsPending || d.Active != 2 {
		t.Fatalf("NOT_READY details = %+v", groupDetails(t, err))
	}
	if st := f.status(string(outside)); !st.Claimable() {
		t.Fatalf("the task outside the group must stay untouched, is %s", st)
	}

	f.complete(s1)
	f.complete(s2)
	// Every member is complete: the proofs wait for a verifier.
	_, err = claimGroup(f, g, false)
	wantCode(t, err, fault.CodeNotReady)
	if d, ok := groupDetails(t, err).(app.GroupNotReady); !ok || !d.GroupProofsPending {
		t.Fatalf("group proofs are due, details = %+v", groupDetails(t, err))
	}
	// Waiting runs the nested group's proofs, then the group's, then DONE.
	_, err = claimGroup(f, g, true)
	wantCode(t, err, fault.CodeDone)
	for _, id := range []task.ID{g, n} {
		if v := f.show(string(id)); v.CompletedAt == nil {
			t.Fatalf("group %s must be complete after its proofs passed: %+v", id, v.GroupVerification)
		}
	}
	_, err = claimGroup(f, g, false)
	wantCode(t, err, fault.CodeDone)
	if st := f.status(string(outside)); !st.Claimable() {
		t.Fatalf("outside task must be untouched, is %s", st)
	}
}

// TestClaimGroupStalledOnOutsideRequirement: a member required by an
// incomplete task outside the group is not pulled in; the group stalls and
// the reason names that task. Completing it releases the member.
func TestClaimGroupStalledOnOutsideRequirement(t *testing.T) {
	f := newGitFixture(t)
	res := f.apply(
		plan.AddTask{Key: "prereq", Title: "prereq", TaskChecks: okChecks()},
		plan.AddGroup{Key: "g", Title: "G"},
		plan.AddTask{Key: "a", Title: "a", Parent: "g", Requires: []plan.Ref{"prereq"}, TaskChecks: okChecks()},
	)
	g, a, prereq := res.Created["g"], res.Created["a"], res.Created["prereq"]

	_, err := claimGroup(f, g, false)
	wantCode(t, err, fault.CodeStalled)
	sum, ok := groupDetails(t, err).(app.Summary)
	if !ok || !sum.Stalled || len(sum.Reasons) == 0 || !strings.Contains(strings.Join(sum.Reasons, "\n"), string(prereq)) {
		t.Fatalf("STALLED must name the outside requirement %s: %+v", prereq, groupDetails(t, err))
	}
	// A waiting claim reports the same stall instead of blocking.
	_, err = claimGroup(f, g, true)
	wantCode(t, err, fault.CodeStalled)

	f.complete(f.claim(string(prereq)))
	s, err := claimGroup(f, g, false)
	if err != nil || s.Task.ID != a {
		t.Fatalf("after the prerequisite completes the member is claimable: %v %v", s.Task.ID, err)
	}
}

// TestClaimGroupWaitVerifiesOnlyThatGroup: a waiting group claim runs the
// proofs of its own subtree, not those of another ready group.
func TestClaimGroupWaitVerifiesOnlyThatGroup(t *testing.T) {
	f := newGitFixture(t)
	res := f.apply(
		plan.AddGroup{Key: "g1", Title: "G1", TaskChecks: []verification.CheckSpec{check("p1", "test -d .", true)}},
		plan.AddTask{Key: "t1", Title: "t1", Parent: "g1", TaskChecks: okChecks()},
		plan.AddGroup{Key: "g2", Title: "G2", TaskChecks: []verification.CheckSpec{check("p2", "test -d .", true)}},
		plan.AddTask{Key: "t2", Title: "t2", Parent: "g2", TaskChecks: okChecks()},
	)
	g1, g2, t1, t2 := res.Created["g1"], res.Created["g2"], res.Created["t1"], res.Created["t2"]
	f.complete(f.claim(string(t1)))
	f.complete(f.claim(string(t2)))

	ctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
	defer cancel()
	f.stampRegression()
	_, err := f.e.Claim(ctx, app.ClaimRequest{GroupID: g1, Wait: true})
	wantCode(t, err, fault.CodeDone)
	if v := f.show(string(g1)); v.CompletedAt == nil {
		t.Fatalf("g1 proofs should have run: %+v", v.GroupVerification)
	}
	if v := f.show(string(g2)); v.CompletedAt != nil || v.GroupVerification.Status != "pending" {
		t.Fatalf("g2 is another group: its proofs must not run: %+v", v.GroupVerification)
	}
}

// TestClaimGroupRejectsNonGroup: only a live group scopes a claim, and not
// beside a task reference.
func TestClaimGroupRejectsNonGroup(t *testing.T) {
	f := newGitFixture(t)
	res := f.apply(
		plan.AddGroup{Key: "g", Title: "G"},
		plan.AddTask{Key: "a", Title: "a", Parent: "g", TaskChecks: okChecks()},
	)
	g, a := res.Created["g"], res.Created["a"]
	_, err := f.e.Claim(f.ctx, app.ClaimRequest{GroupID: a})
	wantCode(t, err, fault.CodeInvalidInput)
	_, err = f.e.Claim(f.ctx, app.ClaimRequest{GroupID: g, TaskID: &a})
	wantCode(t, err, fault.CodeInvalidInput)
}
