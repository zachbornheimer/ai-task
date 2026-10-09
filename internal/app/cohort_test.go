package app_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// cohortPair plans API + UI tasks whose end-to-end check needs both files.
func cohortPair(f *fixture, e2e string) (task.ID, task.ID) {
	res := f.apply(
		plan.AddTask{Key: "api", Title: "API", Cohort: "oauth-flow", TaskChecks: []verification.CheckSpec{check("api-unit", "test -f api.txt", true), check("api-e2e", e2e, true)}},
		plan.AddTask{Key: "ui", Title: "UI", Cohort: "oauth-flow", TaskChecks: []verification.CheckSpec{check("ui-unit", "test -f ui.txt", true)}},
	)
	return res.Created["api"], res.Created["ui"]
}

func TestCoupledVerificationCompletesTogether(t *testing.T) {
	f := newGitFixture(t)
	api, ui := cohortPair(f, "test -f api.txt && test -f ui.txt")
	// Both are claimable at once: a cohort is not a hard dependency.
	sa, su := f.claim(string(api)), f.claim(string(ui))
	f.commit(sa.Workspace, "api.txt", "api")
	f.commit(su.Workspace, "ui.txt", "ui")
	// The first submitter waits: its own checks cannot prove the flow.
	res, err := f.e.Verify(f.ctx, sa.Token, verification.ModeComplete)
	if err != nil || !res.Submitted || res.Completed || res.Status != task.StatusAwaitingVerification {
		t.Fatalf("%+v %v", res, err)
	}
	// Its claim is consumed; the token is not a cohort credential.
	_, err = f.e.Verify(f.ctx, sa.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeSessionFinished)
	sum, _ := f.e.Summary(f.ctx, f.proj.ID)
	if sum.Stalled || sum.Done || sum.Active != 1 {
		t.Fatalf("summary: %+v", sum)
	}
	// The second submitter triggers the cohort job: one candidate, both
	// task suites and the regression suite fresh, both complete, promoted.
	res, err = f.e.Verify(f.ctx, su.Token, verification.ModeComplete)
	if err != nil || !res.Completed {
		t.Fatalf("%+v %v", res, err)
	}
	if f.status(string(api)) != task.StatusComplete || f.status(string(ui)) != task.StatusComplete {
		t.Fatal("members not complete")
	}
	for _, name := range []string{"api.txt", "ui.txt"} {
		if _, err := os.Stat(filepath.Join(f.repo, name)); err != nil {
			t.Fatalf("%s not promoted", name)
		}
	}
	full, _ := f.e.Show(f.ctx, f.proj.ID, string(api), true)
	if len(full.History.Integrations) != 1 || full.History.Integrations[0].JobID == 0 {
		t.Fatalf("%+v", full.History.Integrations)
	}
	done, _ := f.e.Summary(f.ctx, f.proj.ID)
	if !done.Done {
		t.Fatal("not done")
	}
}

func TestCoupledVerificationFailureSendsFailingMemberBack(t *testing.T) {
	f := newGitFixture(t)
	api, ui := cohortPair(f, "test -f api.txt && test -f ui.txt && test \"$(cat ui.txt)\" = ready")
	sa, su := f.claim(string(api)), f.claim(string(ui))
	f.commit(sa.Workspace, "api.txt", "api")
	f.commit(su.Workspace, "ui.txt", "not ready")
	f.e.Verify(f.ctx, su.Token, verification.ModeComplete)
	_, err := f.e.Verify(f.ctx, sa.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeVerificationFailed)
	// api's e2e check failed on the candidate: api is sent back for repair;
	// ui passed its own checks and waits for the peer.
	if f.status(string(api)) != task.StatusVerificationFailed || f.status(string(ui)) != task.StatusAwaitingVerification {
		t.Fatalf("api=%s ui=%s", f.status(string(api)), f.status(string(ui)))
	}
	if _, err := os.Stat(filepath.Join(f.repo, "api.txt")); err == nil {
		t.Fatal("promoted a failing cohort")
	}
	// The real fix is in ui: a planner notices and repairs ui by claiming
	// it (its passing submission is superseded by a new attempt).
	_, err = f.e.Claim(f.ctx, app.ClaimRequest{TaskID: &ui})
	wantCode(t, err, fault.CodeTaskAwaitingVerification)
	// api is repaired and resubmitted unchanged in content; still fails
	// because ui is wrong.
	sa2 := f.claim(string(api))
	_, err = f.e.Verify(f.ctx, sa2.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeVerificationFailed)
	// Planner relaxes api's e2e expectation (contract change) and api is
	// resubmitted: now the cohort passes.
	f.apply(plan.UpdateTask{Target: plan.Ref(api), TaskChecks: plan.Replace([]verification.CheckSpec{check("api-unit", "test -f api.txt", true), check("api-e2e", "test -f api.txt && test -f ui.txt", true)})})
	sa3 := f.claim(string(api))
	res, err := f.e.Verify(f.ctx, sa3.Token, verification.ModeComplete)
	if err != nil || !res.Completed || f.status(string(ui)) != task.StatusComplete {
		t.Fatalf("%+v %v ui=%s", res, err, f.status(string(ui)))
	}
}

func TestCoupledVerificationRecoversAfterVerifierCrash(t *testing.T) {
	f := newGitFixture(t)
	flag := filepath.Join(t.TempDir(), "go")
	api, ui := cohortPair(f, "while [ ! -f "+flag+" ]; do sleep 0.05; done; test -f api.txt && test -f ui.txt")
	sa, su := f.claim(string(api)), f.claim(string(ui))
	f.commit(sa.Workspace, "api.txt", "api")
	f.commit(su.Workspace, "ui.txt", "ui")
	f.e.Verify(f.ctx, sa.Token, verification.ModeComplete)
	// The second submitter's process dies while the job runs.
	_ = f.interruptWhenVerifying(string(ui), "verifying", func(ctx context.Context) error {
		_, err := f.e.Verify(ctx, su.Token, verification.ModeComplete)
		return err
	})
	// Submission was recorded before the job; the job is left running.
	if st := f.status(string(ui)); st != task.StatusVerifying {
		t.Fatalf("after crash: %s", st)
	}
	os.WriteFile(flag, []byte("1"), 0o644)
	f.c.Advance(2 * time.Hour) // verifier lease expires
	sum, _ := f.e.Summary(f.ctx, f.proj.ID)
	if sum.Stalled || len(sum.PendingCohorts) != 1 {
		t.Fatalf("after crash: %+v", sum)
	}
	// Any worker with capacity picks the job up: here via a waiting claim
	// (which runs pending cohorts before deciding done/stalled).
	_, err := f.open().Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID, Wait: true})
	if !fault.Is(err, fault.CodeDone) {
		t.Fatalf("want DONE, got %v", err)
	}
	if f.status(string(api)) != task.StatusComplete || f.status(string(ui)) != task.StatusComplete {
		t.Fatalf("api=%s ui=%s", f.status(string(api)), f.status(string(ui)))
	}
}
