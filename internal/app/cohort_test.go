package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
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
	// Its claim is consumed; the token is not a cohort credential: a second
	// `verify complete` is an acknowledgement, and a write is refused.
	if ack, err := f.e.Verify(f.ctx, sa.Token, verification.ModeComplete); err != nil || !ack.Replayed || !ack.Submitted {
		t.Fatalf("ack: %+v %v", ack, err)
	}
	_, err = f.e.Log(f.ctx, sa.Token, execution.LogEntry{Note: "x"})
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

// A consumed submission token is acknowledged idempotently: the stored
// submission and the member's current status, never new authority or a
// second submission.
func TestCohortSubmissionIsAcknowledgedIdempotently(t *testing.T) {
	f := newGitFixture(t)
	api, ui := cohortPair(f, "test -f api.txt && test -f ui.txt")
	sa, su := f.claim(string(api)), f.claim(string(ui))
	f.commit(sa.Workspace, "api.txt", "api")
	f.commit(su.Workspace, "ui.txt", "ui")
	first, err := f.e.Verify(f.ctx, sa.Token, verification.ModeComplete)
	if err != nil || !first.Submitted {
		t.Fatal(err)
	}
	ack, err := f.e.Verify(f.ctx, sa.Token, verification.ModeComplete)
	if err != nil || !ack.Replayed || !ack.Submitted || ack.SubmissionID != first.SubmissionID || ack.Status != task.StatusAwaitingVerification || ack.Completed {
		t.Fatalf("ack: %+v %v", ack, err)
	}
	full, _ := f.e.Show(f.ctx, f.proj.ID, string(api), true)
	if len(full.History.Submissions) != 1 || full.Failures != 0 {
		t.Fatalf("acknowledgement submitted again: %d submissions", len(full.History.Submissions))
	}
	// The consumed token still cannot act.
	if _, err := f.e.Log(f.ctx, sa.Token, execution.LogEntry{Note: "x"}); fault.CodeOf(err) != fault.CodeSessionFinished {
		t.Fatalf("consumed token acted: %v", err)
	}
	if _, err := f.e.Verify(f.ctx, su.Token, verification.ModeComplete); err != nil {
		t.Fatal(err)
	}
	ack, err = f.e.Verify(f.ctx, sa.Token, verification.ModeComplete)
	if err != nil || !ack.Replayed || !ack.Completed || ack.Status != task.StatusComplete {
		t.Fatalf("ack after completion: %+v %v", ack, err)
	}
}

// A submission waiting for its peers pins the member's contract; the
// planner changes it only by withdrawing the submission explicitly, which
// counts no failure and makes the member resubmit under the new contract.
func TestSubmittedCohortContractIsPinned(t *testing.T) {
	f := newGitFixture(t)
	api, ui := cohortPair(f, "test -f api.txt && test -f ui.txt")
	sa, su := f.claim(string(api)), f.claim(string(ui))
	f.commit(sa.Workspace, "api.txt", "api")
	f.commit(su.Workspace, "ui.txt", "ui")
	if _, err := f.e.Verify(f.ctx, sa.Token, verification.ModeComplete); err != nil {
		t.Fatal(err)
	}
	relaxed := plan.Replace([]verification.CheckSpec{check("api-unit", "test -f api.txt", true)})
	_, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Planner: true, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(api), TaskChecks: relaxed}}})
	wantCode(t, err, fault.CodePlanConflict)
	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(api), TaskChecks: relaxed, WithdrawSubmission: true}}})
	wantCode(t, err, fault.CodePlanConflict) // withdrawal needs planner authority
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Planner: true, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(api), TaskChecks: relaxed, WithdrawSubmission: true}}}); err != nil {
		t.Fatal(err)
	}
	v := f.show(string(api))
	if !v.Status.Claimable() || v.Failures != 0 || v.LastError == "" || len(v.Checks) != 1 {
		t.Fatalf("after withdrawal: %s failures=%d last_error=%q checks=%d", v.Status, v.Failures, v.LastError, len(v.Checks))
	}
	// The peer's submission does not start a job while api is withdrawn.
	if res, err := f.e.Verify(f.ctx, su.Token, verification.ModeComplete); err != nil || res.Completed || f.status(string(ui)) != task.StatusAwaitingVerification {
		t.Fatalf("%+v %v", res, err)
	}
	sa2 := f.claim(string(api))
	res, err := f.e.Verify(f.ctx, sa2.Token, verification.ModeComplete)
	if err != nil || !res.Completed || f.status(string(ui)) != task.StatusComplete {
		t.Fatalf("%+v %v", res, err)
	}
}

// A late conflict on one member (a prerequisite added after it submitted)
// sends only that member back; its peers keep their submissions, and no
// failure is counted.
func TestCohortLateConflictBlamesOnlyThatMember(t *testing.T) {
	f := newGitFixture(t)
	api, ui := cohortPair(f, "test -f api.txt && test -f ui.txt")
	sa, su := f.claim(string(api)), f.claim(string(ui))
	f.commit(sa.Workspace, "api.txt", "api")
	f.commit(su.Workspace, "ui.txt", "ui")
	if _, err := f.e.Verify(f.ctx, sa.Token, verification.ModeComplete); err != nil {
		t.Fatal(err)
	}
	pre := f.apply(plan.AddTask{Key: "pre", Title: "pre", Blocks: []plan.Ref{plan.Ref(api)}, TaskChecks: okChecks()}).Created["pre"]
	res, err := f.e.Verify(f.ctx, su.Token, verification.ModeComplete)
	if err != nil && fault.CodeOf(err) == fault.CodeInternal {
		t.Fatal(err)
	}
	if res.Completed {
		t.Fatal("completed with an unmet prerequisite")
	}
	va, vu := f.show(string(api)), f.show(string(ui))
	if va.Status != task.StatusBlocked || va.Failures != 0 || va.LastError == "" {
		t.Fatalf("api: %s failures=%d last_error=%q", va.Status, va.Failures, va.LastError)
	}
	if vu.Status != task.StatusAwaitingVerification || vu.Failures != 0 || vu.LastError != "" {
		t.Fatalf("ui: %s failures=%d last_error=%q", vu.Status, vu.Failures, vu.LastError)
	}
	// Completing the prerequisite and resubmitting api finishes the cohort.
	f.complete(f.claim(string(pre)))
	sa2 := f.claim(string(api))
	if res, err := f.e.Verify(f.ctx, sa2.Token, verification.ModeComplete); err != nil || !res.Completed || f.status(string(ui)) != task.StatusComplete {
		t.Fatalf("%+v %v", res, err)
	}
}

// A shared regression failure on the combined candidate sends every
// member back with the evidence but charges nobody.
func TestCohortRegressionFailureChargesNobody(t *testing.T) {
	f := newGitFixture(t)
	f.setRegression(check("regress", "test ! -f ui.txt", true))
	api, ui := cohortPair(f, "test -f api.txt && test -f ui.txt")
	sa, su := f.claim(string(api)), f.claim(string(ui))
	f.commit(sa.Workspace, "api.txt", "api")
	f.commit(su.Workspace, "ui.txt", "ui")
	f.e.Verify(f.ctx, sa.Token, verification.ModeComplete)
	_, err := f.e.Verify(f.ctx, su.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeVerificationFailed)
	for _, id := range []task.ID{api, ui} {
		v := f.show(string(id))
		if v.Status != task.StatusVerificationFailed || v.Failures != 0 || !strings.Contains(v.LastError, "no single member") {
			t.Fatalf("%s: %s failures=%d last_error=%q", id, v.Status, v.Failures, v.LastError)
		}
	}
}

// A cohort whose promotion keeps failing for environmental reasons backs
// off (bounded retries, no hot loop), is reported as cooling rather than
// stalled, lets unrelated work proceed, and completes once the cause is
// gone.
func TestCohortPromotionBackoffIsBounded(t *testing.T) {
	f := newGitFixture(t)
	api, ui := cohortPair(f, "test -f api.txt && test -f ui.txt")
	other := f.add("other")
	sa, su := f.claim(string(api)), f.claim(string(ui))
	f.commit(sa.Workspace, "api.txt", "api")
	f.commit(su.Workspace, "ui.txt", "ui")
	f.e.Verify(f.ctx, sa.Token, verification.ModeComplete)
	app.SetFaultHook(f.e, func(p string) error {
		if p == "cohort:promote" {
			return fault.New(fault.CodeIntegrationFailed, "target checkout is unusable (simulated)")
		}
		return nil
	})
	if res, err := f.e.Verify(f.ctx, su.Token, verification.ModeComplete); err != nil || res.Completed {
		t.Fatalf("%+v %v", res, err)
	}
	jobs := func() int {
		full, _ := f.e.Show(f.ctx, f.proj.ID, string(api), true)
		n := 0
		for _, r := range full.History.Runs {
			if r.Mode == verification.ModeCohort {
				n++
			}
		}
		return n
	}
	if jobs() != 1 {
		t.Fatalf("jobs after first failure: %d", jobs())
	}
	// Immediately retrying does nothing: the cohort is backing off.
	if ran, _ := f.e.RunPendingCohorts(f.ctx, f.proj.ID); ran || jobs() != 1 {
		t.Fatalf("retried without backoff: ran=%v jobs=%d", ran, jobs())
	}
	sum, _ := f.e.Summary(f.ctx, f.proj.ID)
	if sum.Stalled || sum.Cooling == 0 || sum.NextEligibleAt == nil {
		t.Fatalf("summary during backoff: %+v", sum)
	}
	// Unrelated work proceeds meanwhile.
	f.complete(f.claim(string(other)))
	// Backoff grows: 30s, then 1m.
	f.c.Advance(31 * time.Second)
	if ran, _ := f.e.RunPendingCohorts(f.ctx, f.proj.ID); !ran || jobs() != 2 {
		t.Fatalf("second try: ran=%v jobs=%d", ran, jobs())
	}
	f.c.Advance(31 * time.Second)
	if ran, _ := f.e.RunPendingCohorts(f.ctx, f.proj.ID); ran || jobs() != 2 {
		t.Fatalf("retried before 1m: ran=%v jobs=%d", ran, jobs())
	}
	f.c.Advance(30 * time.Second)
	if ran, _ := f.e.RunPendingCohorts(f.ctx, f.proj.ID); !ran || jobs() != 3 {
		t.Fatalf("third try: ran=%v jobs=%d", ran, jobs())
	}
	for _, id := range []task.ID{api, ui} {
		if v := f.show(string(id)); v.Failures != 0 || v.LastError == "" || v.Status != task.StatusAwaitingVerification {
			t.Fatalf("%s during backoff: %s failures=%d last_error=%q", id, v.Status, v.Failures, v.LastError)
		}
	}
	// The cause is fixed: the next retry completes without anyone resubmitting.
	app.SetFaultHook(f.e, nil)
	f.c.Advance(3 * time.Minute)
	if ran, err := f.e.RunPendingCohorts(f.ctx, f.proj.ID); !ran || err != nil {
		t.Fatalf("final try: ran=%v %v", ran, err)
	}
	if f.status(string(api)) != task.StatusComplete || f.status(string(ui)) != task.StatusComplete {
		t.Fatalf("api=%s ui=%s", f.status(string(api)), f.status(string(ui)))
	}
}
