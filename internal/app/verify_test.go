package app_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func TestBareVerifyIsInvalid(t *testing.T) {
	f := newGitFixture(t)
	s := f.claim(string(f.add("a")))
	_, err := f.e.Verify(f.ctx, s.Token, "")
	wantCode(t, err, fault.CodeInvalidInput)
}

func TestEmptyChecksFailClosed(t *testing.T) {
	f := newGitFixture(t)
	// A task without checks cannot be planned, and checks cannot be removed.
	_, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.AddTask{Key: "nc", Title: "no checks"}}})
	wantCode(t, err, fault.CodeMissingVerification)
	a := f.add("a")
	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(a), TaskChecks: plan.Replace([]verification.CheckSpec{})}}})
	wantCode(t, err, fault.CodeMissingVerification)
	// A project without regression checks hands out no work.
	f.setRegression()
	_, err = f.e.Claim(f.ctx, app.ClaimRequest{TaskID: &a})
	wantCode(t, err, fault.CodeMissingVerification)
	_, err = f.e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID})
	wantCode(t, err, fault.CodeMissingVerification)
	// Regression checks removed under a live claim: verification still fails
	// closed and the claim survives.
	f.setRegression(check("regress", "true", true))
	sa := f.claim(string(a))
	f.setRegression()
	f.commit(sa.Workspace, "a.txt", "a")
	_, err = f.e.Verify(f.ctx, sa.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeMissingVerification)
	_, err = f.e.Verify(f.ctx, sa.Token, verification.ModeRegression)
	wantCode(t, err, fault.CodeMissingVerification)
	if f.status(string(a)) != task.StatusClaimed {
		t.Fatal("claim must survive a fail-closed refusal")
	}
}

func TestProvisionalChecksRunFreshAndNeverComplete(t *testing.T) {
	f := newGitFixture(t)
	counter := filepath.Join(t.TempDir(), "count")
	runs := func() int { b, _ := os.ReadFile(counter); return strings.Count(string(b), "x") }
	a := f.addWith("a", "echo x >> "+counter)
	s := f.claim(string(a))
	// Uncommitted edits are fine for diagnostics.
	os.WriteFile(filepath.Join(s.Workspace, "wip.txt"), []byte("wip"), 0o644)
	for i := 1; i <= 3; i++ {
		res, err := f.e.Verify(f.ctx, s.Token, verification.ModeTask)
		if err != nil || !res.Passed || res.Completed || runs() != i {
			t.Fatalf("run %d: %+v %v runs=%d", i, res, err, runs())
		}
	}
	res, err := f.e.Verify(f.ctx, s.Token, verification.ModeRegression)
	if err != nil || !res.Passed || res.Completed || res.Mode != verification.ModeRegression {
		t.Fatalf("%+v %v", res, err)
	}
	if st := f.status(string(a)); st != task.StatusClaimed {
		t.Fatalf("diagnostics changed status to %s", st)
	}
	v := f.show(string(a))
	if v.Verification.Task == nil || v.Verification.Regression == nil || v.Verification.Complete != nil || v.Attempt == nil || !v.Attempt.LeaseActive {
		t.Fatalf("%+v", v.Verification)
	}
	// Failing diagnostics report VERIFICATION_FAILED with details and keep
	// the claim live.
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Planner: true, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(a), TaskChecks: plan.Replace([]verification.CheckSpec{check("unit-a", "echo x >> "+counter+"; exit 3", true)})}}}); err != nil {
		t.Fatal(err)
	}
	res, err = f.e.Verify(f.ctx, s.Token, verification.ModeTask)
	wantCode(t, err, fault.CodeVerificationFailed)
	if res.Passed || runs() != 4 || res.Evidence[0].ExitCode != 3 || f.status(string(a)) != task.StatusClaimed {
		t.Fatalf("%+v runs=%d", res, runs())
	}
}

func TestNoEvidenceReuseAndSameSnapshot(t *testing.T) {
	f := newGitFixture(t)
	counter := filepath.Join(t.TempDir(), "count")
	runs := func() int { b, _ := os.ReadFile(counter); return strings.Count(string(b), "x") }
	// The check reads the snapshot's file; the agent keeps editing its
	// worktree during verification. Evidence must describe the commit.
	a := f.apply(plan.AddTask{Key: "a", Title: "a", TaskChecks: []verification.CheckSpec{check("unit-a", "echo x >> "+counter+"; test \"$(cat f.txt)\" = committed", true)}}).Created["a"]
	s := f.claim(string(a))
	f.commit(s.Workspace, "f.txt", "committed")
	res := f.complete(s)
	if runs() != 1 || res.Revision == "" {
		t.Fatalf("%+v runs=%d", res, runs())
	}
	// A second task with the identical check on the identical content runs
	// the check again: no cross-run reuse.
	b := f.apply(plan.AddTask{Key: "b", Title: "b", TaskChecks: []verification.CheckSpec{check("unit-a", "echo x >> "+counter+"; test \"$(cat f.txt)\" = committed", true)}}).Created["b"]
	sb := f.claim(string(b))
	resB := f.complete(sb)
	if runs() != 2 {
		t.Fatalf("evidence reused: runs=%d", runs())
	}
	for _, ev := range resB.Evidence {
		if ev.Reused {
			t.Fatal("reused evidence row")
		}
	}
	// Same snapshot: the regression check sees exactly the committed content
	// even though the worktree is edited after the commit.
	c := f.apply(plan.AddTask{Key: "c", Title: "c", TaskChecks: []verification.CheckSpec{check("unit-c", "test \"$(cat f.txt)\" = committed-c", true)}}).Created["c"]
	sc := f.claim(string(c))
	f.commit(sc.Workspace, "f.txt", "committed-c")
	os.WriteFile(filepath.Join(sc.Workspace, "f.txt"), []byte("edited after commit"), 0o644)
	// Dirty tree: refused outright.
	_, err := f.e.Verify(f.ctx, sc.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeWorkspaceDirty)
	os.WriteFile(filepath.Join(sc.Workspace, "f.txt"), []byte("committed-c"), 0o644)
	f.complete(sc)
}

func TestFinalSuccessIsAtomicAndPromotes(t *testing.T) {
	f := newGitFixture(t)
	a := f.add("a")
	d := f.add("d", "a")
	s := f.claim(string(a))
	rev := f.commit(s.Workspace, "a.txt", "a")
	res, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	if err != nil || !res.Completed || res.Revision != rev || res.Status != task.StatusComplete {
		t.Fatalf("%+v %v", res, err)
	}
	// Claim ended atomically with completion.
	_, err = f.e.Log(f.ctx, s.Token, execution.LogEntry{Done: "late"})
	wantCode(t, err, fault.CodeSessionFinished)
	// Promoted to main: the main worktree has the file.
	if b, _ := os.ReadFile(filepath.Join(f.repo, "a.txt")); string(b) != "a" {
		t.Fatal("not promoted")
	}
	if f.git(f.repo, "rev-parse", "main") != rev {
		t.Fatal("main not at the verified revision")
	}
	v := f.show(string(a))
	if v.CompletedRevision != rev || v.Verification.Complete == nil || v.Verification.Complete.Status != "passed" {
		t.Fatalf("%+v", v)
	}
	// Downstream is released and its worktree starts from the promoted main.
	sd := f.claim(string(d))
	if b, _ := os.ReadFile(filepath.Join(sd.Workspace, "a.txt")); string(b) != "a" {
		t.Fatal("dependent worktree lacks the prerequisite's change")
	}
	// Idempotent acknowledgement: same token after completion returns the
	// stored result without a new run.
	before := f.show(string(a)).Verification.Complete.ID
	again, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	if err != nil || !again.Replayed || !again.Completed || again.RunID != before {
		t.Fatalf("%+v %v", again, err)
	}
	// Full history shows the integration record.
	full, _ := f.e.Show(f.ctx, f.proj.ID, string(a), true)
	if full.History == nil || len(full.History.Integrations) != 1 || full.History.Integrations[0].ResultRevision != rev {
		t.Fatalf("%+v", full.History)
	}
}

func TestFinalFailureKeepsClaimAndBlocksDownstream(t *testing.T) {
	f := newGitFixture(t)
	a := f.addWith("a", "exit 1")
	d := f.add("d", "a")
	s := f.claim(string(a))
	f.commit(s.Workspace, "a.txt", "a")
	res, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeVerificationFailed)
	if res.Completed || res.Status != task.StatusClaimed {
		t.Fatalf("%+v", res)
	}
	if f.status(string(d)) != task.StatusBlocked {
		t.Fatal("downstream released by a failed verification")
	}
	if f.git(f.repo, "rev-parse", "main") == f.git(s.Workspace, "rev-parse", "HEAD") {
		t.Fatal("promoted a failing revision")
	}
	// The claim is live: the planner relaxes the check, the agent commits a
	// fix and verifies again -> complete; a second submission supersedes.
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Planner: true, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(a), TaskChecks: plan.Replace([]verification.CheckSpec{check("unit-a", "true", true)})}}}); err != nil {
		t.Fatal(err)
	}
	f.commit(s.Workspace, "fix.txt", "fix")
	res2, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	if err != nil || !res2.Completed || res2.SubmissionID == res.SubmissionID {
		t.Fatalf("%+v %v", res2, err)
	}
	if f.status(string(d)) != task.StatusReady {
		t.Fatal("downstream not released")
	}
}

func TestLateBlockerPreventsCompletion(t *testing.T) {
	f := newGitFixture(t)
	a := f.add("a")
	s := f.claim(string(a))
	// Discovered blocker added by the holder while working.
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Session: string(s.Token), Operations: []plan.Change{plan.AddTask{Key: "pre", Title: "pre", Blocks: []plan.Ref{plan.Ref(a)}, TaskChecks: []verification.CheckSpec{check("u", "true", true)}}}}); err != nil {
		t.Fatal(err)
	}
	f.commit(s.Workspace, "a.txt", "a")
	_, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeTaskBlocked)
	if f.status(string(a)) != task.StatusClaimed {
		t.Fatal("claim lost")
	}
	// Release, complete the prerequisite, resume and complete.
	f.e.Release(f.ctx, s.Token, app.ReleaseOptions{Note: "waiting on pre"})
	if f.status(string(a)) != task.StatusBlocked {
		t.Fatal("not blocked")
	}
	f.complete(f.claim("pre"))
	s2 := f.claim(string(a))
	if s2.Handoff.RecentLogs[0].Note != "waiting on pre" {
		t.Fatal("handoff lost")
	}
	f.complete(s2)
}

func TestCompletedContractsCannotChangeSilently(t *testing.T) {
	f := newGitFixture(t)
	a, b := f.add("a"), f.add("b")
	f.complete(f.claim(string(a)))
	// A hard requirement on a complete task is rejected.
	_, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(a), AddRequires: []plan.Ref{plan.Ref(b)}}}})
	wantCode(t, err, fault.CodePlanConflict)
	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.AddTask{Title: "x", Blocks: []plan.Ref{plan.Ref(a)}, TaskChecks: okChecks()}}})
	wantCode(t, err, fault.CodePlanConflict)
	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(a), Title: strptr("renamed")}}})
	wantCode(t, err, fault.CodePlanConflict)
	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.ArchiveTask{Target: plan.Ref(a), Reason: "test"}}})
	wantCode(t, err, fault.CodePlanConflict)
	if f.status(string(a)) != task.StatusComplete {
		t.Fatal("completion lost")
	}
	// Contract change during a running verification fails closed: simulate
	// with a check that edits the contract through the planner mid-run.
}

func TestContractChangeDuringVerificationFailsClosed(t *testing.T) {
	f := newGitFixture(t)
	flag := filepath.Join(t.TempDir(), "go")
	a := f.addWith("a", "while [ ! -f "+flag+" ]; do sleep 0.05; done")
	s := f.claim(string(a))
	f.commit(s.Workspace, "a.txt", "a")
	done := make(chan error, 1)
	go func() {
		_, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
		done <- err
	}()
	deadline := time.Now().Add(15 * time.Second)
	for f.status(string(a)) != task.StatusVerifying && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if st := f.status(string(a)); st != task.StatusVerifying {
		t.Fatalf("status during run: %s", st)
	}
	// Planner tightens the contract while the run is in flight.
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Planner: true, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(a), Acceptance: plan.Replace([]string{"stricter"})}}}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(flag, []byte("1"), 0o644)
	err := <-done
	wantCode(t, err, fault.CodePlanConflict)
	if f.status(string(a)) == task.StatusComplete {
		t.Fatal("completed under a changed contract")
	}
}

func TestVerifierCrashRecovery(t *testing.T) {
	f := newGitFixture(t)
	flag := filepath.Join(t.TempDir(), "go")
	a := f.addWith("a", "while [ ! -f "+flag+" ]; do sleep 0.05; done")
	s := f.claim(string(a))
	f.commit(s.Workspace, "a.txt", "a")
	// Simulate: the run was recorded running, then the process died.
	err := f.interruptWhenVerifying(string(a), "verifying", func(ctx context.Context) error {
		_, err := f.e.Verify(ctx, s.Token, verification.ModeComplete)
		return err
	})
	if err == nil {
		t.Fatal("expected interruption")
	}
	if st := f.status(string(a)); st != task.StatusVerifying {
		t.Fatalf("run should still be recorded running right after the crash: %s", st)
	}
	os.WriteFile(flag, []byte("1"), 0o644)
	f.c.Advance(time.Hour)
	// Reconcile: no phantom "verifying"; another worker resumes.
	if st := f.status(string(a)); st == task.StatusVerifying {
		t.Fatal("phantom verifying state")
	}
	s2, err := f.open().Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID})
	if err != nil || s2.Task.ID != a || s2.AttemptSeq != 2 {
		t.Fatalf("%+v %v", s2, err)
	}
	// The new attempt's worktree continues from the previous branch.
	if b, _ := os.ReadFile(filepath.Join(s2.Workspace, "a.txt")); string(b) != "a" {
		t.Fatal("work not carried over")
	}
	f.complete(s2)
}

func TestIntegrationConflictDoesNotComplete(t *testing.T) {
	f := newGitFixture(t)
	a := f.add("a")
	s := f.claim(string(a))
	f.commit(s.Workspace, "f.txt", "from a")
	// main moves with a conflicting change before a verifies.
	f.commit(f.repo, "f.txt", "from main")
	res, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeIntegrationFailed)
	if f.status(string(a)) == task.StatusComplete || f.git(f.repo, "rev-parse", "HEAD") != f.git(f.repo, "rev-parse", "main") {
		t.Fatal("completed or moved target despite conflict")
	}
	// The refusal names the conflicting files and the remedy, and no check
	// ran: the merge is checked before any check time is spent.
	if len(res.Conflicts) != 1 || res.Conflicts[0] != "f.txt" || !strings.Contains(res.Message, "git merge main") || len(res.Evidence) != 0 {
		t.Fatalf("conflict result: %+v", res)
	}
	if v := f.show(string(a)); v.Failures != 0 || !strings.Contains(v.LastError, "f.txt") {
		t.Fatalf("conflict must be recorded without a strike: failures=%d last_error=%q", v.Failures, v.LastError)
	}
	if b, _ := os.ReadFile(filepath.Join(f.repo, "f.txt")); string(b) != "from main" {
		t.Fatal("target content changed")
	}
	// Non-conflicting divergence: merged candidate is verified, then promoted.
	b := f.add("b")
	sb := f.claim(string(b))
	f.commit(sb.Workspace, "b.txt", "b")
	f.commit(f.repo, "other.txt", "main again")
	res, err = f.e.Verify(f.ctx, sb.Token, verification.ModeComplete)
	if err != nil || !res.Completed || res.IntegratedRevision == "" || res.IntegratedRevision == res.Revision {
		t.Fatalf("%+v %v", res, err)
	}
	if f.git(f.repo, "rev-parse", "main") != res.IntegratedRevision {
		t.Fatal("main not at merged candidate")
	}
	// Evidence exists for both the submitted revision and the candidate.
	revs := map[string]bool{}
	for _, ev := range res.Evidence {
		revs[ev.Revision] = true
	}
	if !revs[res.Revision] || !revs[res.IntegratedRevision] {
		t.Fatalf("evidence revisions: %v", revs)
	}
}

func TestGoTestCacheDisabledInCheckEnvironment(t *testing.T) {
	f := newGitFixture(t)
	a := f.addWith("a", "test \"$GOFLAGS\" = \"-count=1\" && test -z \"$AT_SESSION\"")
	s := f.claim(string(a))
	f.commit(s.Workspace, "a.txt", "a")
	t.Setenv("AT_SESSION", string(s.Token))
	f.complete(s)
}

// A policy made only of optional checks can never fail, so it can never be
// a gate: planning, project configuration, claiming and completion all
// refuse it, including rows written before the rule existed.
func TestVacuousPoliciesFailClosed(t *testing.T) {
	f := newGitFixture(t)
	opt := func(id string) verification.CheckSpec { return check(id, "false", false) }
	optJSON := func(ids ...string) string {
		var cs []verification.CheckSpec
		for _, id := range ids {
			cs = append(cs, opt(id))
		}
		b, _ := json.Marshal(verification.Policy{TaskChecks: cs})
		return string(b)
	}
	// Planning boundaries.
	_, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.AddTask{Key: "o", Title: "o", TaskChecks: []verification.CheckSpec{opt("o1")}}}})
	wantCode(t, err, fault.CodeMissingVerification)
	a := f.add("a")
	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(a), TaskChecks: plan.Replace([]verification.CheckSpec{opt("o2")})}}})
	wantCode(t, err, fault.CodeMissingVerification)
	// Project boundary.
	p, _ := f.e.Project(f.ctx, f.proj.ID)
	p.Regression = []verification.CheckSpec{opt("r")}
	wantCode(t, f.e.UpdateProject(f.ctx, p), fault.CodeMissingVerification)
	// Legacy rows that bypassed the rule: never handed out, never completed.
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	setPolicy := func(id task.ID, body string) {
		t.Helper()
		if _, err := db.Exec(`UPDATE tasks SET policy_json = ? WHERE id = ?`, body, id); err != nil {
			t.Fatal(err)
		}
	}
	setRegression := func(body string) {
		t.Helper()
		if _, err := db.Exec(`UPDATE projects SET regression_json = ? WHERE id = ?`, body, f.proj.ID); err != nil {
			t.Fatal(err)
		}
	}
	regOpt, _ := json.Marshal(verification.Policy{Regression: []verification.CheckSpec{opt("r")}})
	setRegression(string(regOpt))
	_, err = f.e.Claim(f.ctx, app.ClaimRequest{TaskID: &a})
	wantCode(t, err, fault.CodeMissingVerification)
	_, err = f.e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID})
	wantCode(t, err, fault.CodeMissingVerification)
	f.setRegression(check("regress", "true", true))
	setPolicy(a, optJSON("o3"))
	if st := f.status(string(a)); st != task.StatusNeedsAttention {
		t.Fatalf("vacuous task status %s", st)
	}
	snap, _ := f.e.List(f.ctx, app.ListQuery{ProjectID: f.proj.ID, Filter: app.FilterReady})
	for _, v := range snap.Tasks {
		if v.ID == a {
			t.Fatal("vacuous task listed as ready")
		}
	}
	_, err = f.e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID})
	wantCode(t, err, fault.CodeNoAvailableTask)
	_, err = f.e.Claim(f.ctx, app.ClaimRequest{TaskID: &a})
	wantCode(t, err, fault.CodeMissingVerification)
	// Checks made vacuous underneath a live claim: completion still refuses.
	b := f.add("b")
	s := f.claim(string(b))
	f.commit(s.Workspace, "b.txt", "b")
	setPolicy(b, optJSON("o4"))
	_, err = f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeMissingVerification)
	_, err = f.e.Verify(f.ctx, s.Token, verification.ModeTask)
	wantCode(t, err, fault.CodeMissingVerification)
	good, _ := json.Marshal(verification.Policy{TaskChecks: []verification.CheckSpec{check("u", "true", true)}})
	setPolicy(b, string(good))
	setRegression(string(regOpt))
	_, err = f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeMissingVerification)
	_, err = f.e.Verify(f.ctx, s.Token, verification.ModeRegression)
	wantCode(t, err, fault.CodeMissingVerification)
	if f.status(string(b)) != task.StatusClaimed {
		t.Fatal("claim must survive the refusal")
	}
	// With real gates on both sides the same task completes normally.
	f.setRegression(check("regress", "true", true))
	f.complete(s)
}

// Check categories run on separate snapshots: a file a task check leaves
// behind is not there for the regression suite, which therefore proves
// the committed revision only. The session token never reaches a check.
func TestCategoriesDoNotShareASnapshot(t *testing.T) {
	f := newGitFixture(t)
	f.setRegression(check("regress", "test -f artifact.txt", true))
	a := f.apply(plan.AddTask{Key: "a", Title: "a", TaskChecks: []verification.CheckSpec{
		check("leave", "echo made > artifact.txt && test -z \"$AT_SESSION\" && test -n \"$TMPDIR\" && test \"$TMPDIR\" != \"$AT_CHECK_DIR\"", true),
	}}).Created["a"]
	s := f.claim(string(a))
	f.commit(s.Workspace, "a.txt", "a")
	res, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeVerificationFailed)
	var leave, regress verification.Outcome
	for _, ev := range res.Evidence {
		switch ev.CheckID {
		case "leave":
			leave = ev.Outcome
		case "regress":
			regress = ev.Outcome
		}
	}
	if leave != verification.OutcomePassed || regress != verification.OutcomeFailed {
		t.Fatalf("leave=%s regress=%s (the regression suite saw the task check's artifact)", leave, regress)
	}
	// Committing the artifact makes it part of the revision: both pass.
	f.commit(s.Workspace, "artifact.txt", "committed")
	if res, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete); err != nil || !res.Completed {
		t.Fatalf("%+v %v", res, err)
	}
}

// A diagnostic run left `running` by a dead process is closed once its
// attempt is gone, without any task failure.
func TestOrphanedDiagnosticRunsAreReconciled(t *testing.T) {
	f := newGitFixture(t)
	a := f.add("a")
	s := f.claim(string(a))
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var attemptID int64
	if err := db.QueryRow(`SELECT id FROM execution_attempts WHERE task_id = ? ORDER BY seq DESC LIMIT 1`, a).Scan(&attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO verification_runs (task_id, attempt_id, mode, status, policy_digest, created_at, started_at) VALUES (?, ?, 'task', 'running', 'x', 1, 1)`, a, attemptID); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.e.Show(f.ctx, f.proj.ID, string(a), true); v.Verification.Task == nil || v.Verification.Task.Status != sqlite.RunRunning {
		t.Fatal("setup: diagnostic run not visible as running")
	}
	f.e.Release(f.ctx, s.Token, app.ReleaseOptions{})
	v, _ := f.e.Show(f.ctx, f.proj.ID, string(a), true)
	if v.Verification.Task == nil || v.Verification.Task.Status != sqlite.RunError || v.Failures != 0 {
		t.Fatalf("orphaned diagnostic run: %+v failures=%d", v.Verification.Task, v.Failures)
	}
}
