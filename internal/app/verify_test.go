package app_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func sh(script string) []string { return []string{"sh", "-c", script} }

func check(id, script string, required bool) verification.CheckSpec {
	return verification.CheckSpec{ID: id, Command: sh(script), Required: required, Timeout: 20 * time.Second}
}

// gitFixture is a fixture whose project root is a real Git repository.
type gitFixture struct {
	*fixture
	repo string
}

func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	f := &fixture{t: t, ctx: context.Background(), c: newClock(), path: filepath.Join(t.TempDir(), "tasks.db")}
	f.e = f.open()
	g := &gitFixture{fixture: f, repo: filepath.Join(t.TempDir(), "repo")}
	os.MkdirAll(g.repo, 0o755)
	g.git("init", "-q", "-b", "main")
	g.commit("one")
	var err error
	f.proj, err = f.e.InitProject(f.ctx, "gitproj", g.repo)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func (g *gitFixture) git(args ...string) string {
	g.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", g.repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes a file and commits, returning the new HEAD.
func (g *gitFixture) commit(content string) string {
	g.t.Helper()
	os.WriteFile(filepath.Join(g.repo, "f.txt"), []byte(content), 0o644)
	g.git("add", "f.txt")
	g.git("commit", "-q", "-m", content)
	return g.git("rev-parse", "HEAD")
}

func (f *fixture) addWithPolicy(desc string, p verification.Policy) task.Task {
	f.t.Helper()
	tk, _, err := f.e.Add(f.ctx, task.Spec{ProjectID: f.proj.ID, Description: desc, Verification: p})
	if err != nil {
		f.t.Fatal(err)
	}
	return tk
}

func (f *fixture) takeAndFinish(id task.ID, opts app.FinishOptions) (app.SubmissionResult, error) {
	f.t.Helper()
	s, err := f.e.Take(f.ctx, app.TakeRequest{Task: &id})
	if err != nil {
		f.t.Fatal(err)
	}
	return f.e.Finish(f.ctx, s.Token, opts)
}

func TestFailingRequiredCheckPreventsCompletionAndPreservesWork(t *testing.T) {
	g := newGitFixture(t)
	f := g.fixture
	a := f.addWithPolicy("needs proof", verification.Policy{TaskChecks: []verification.CheckSpec{check("unit", "echo boom >&2; exit 2", true)}})
	b := f.add("dependent")
	f.e.AddDependency(f.ctx, b.ID, a.ID)
	s, _ := f.e.Take(f.ctx, app.TakeRequest{Task: &a.ID})
	f.e.Log(f.ctx, s.Token, executionLogEntry{Done: "tried", Next: "fix the thing"})
	res, err := f.e.Finish(f.ctx, s.Token, app.FinishOptions{})
	wantCode(t, err, fault.CodeVerificationFailed)
	if res.Verification != "failed" || res.Status != task.StatusVerificationFailed || res.SubmissionID == 0 || len(res.Revision) != 40 {
		t.Fatalf("result: %+v", res)
	}
	var fe *fault.Error
	if !asFaultErr(err, &fe) || fe.Details == nil {
		t.Fatalf("details missing: %v", err)
	}
	if f.status(b.ID) != task.StatusBlocked {
		t.Fatal("failed verification must not release dependents")
	}
	page, err := f.e.Evidence(f.ctx, a.ID, 0)
	if err != nil || len(page.Runs) != 1 || len(page.Runs[0].Evidence) != 1 {
		t.Fatalf("evidence: %+v %v", page, err)
	}
	ev := page.Runs[0].Evidence[0]
	if ev.Outcome != verification.OutcomeFailed || ev.ExitCode != 2 || !strings.Contains(ev.Stderr, "boom") || ev.Revision != res.Revision || ev.PolicyDigest != res.PolicyDigest {
		t.Fatalf("evidence row: %+v", ev)
	}
	// Work and handoff survive; the task is takeable again for repair.
	v, _ := f.e.Show(f.ctx, a.ID)
	if v.Handoff.LatestNext != "fix the thing" || v.Submission == nil || v.Submission.Verification != "failed" {
		t.Fatalf("view: %+v", v)
	}
	s2, err := f.e.Take(f.ctx, app.TakeRequest{Project: f.proj.ID})
	if err != nil || s2.TaskID != a.ID || s2.AttemptSeq != 2 {
		t.Fatalf("repair take: %+v %v", s2, err)
	}
	if f.status(a.ID) != task.StatusInProgress {
		t.Fatal("active attempt outranks the failed verification")
	}
	v2, _ := f.e.Show(f.ctx, a.ID)
	if v2.Submission == nil || v2.Submission.Verification != "failed" {
		t.Fatal("failure details must remain visible during the repair attempt")
	}
}

func TestOptionalCheckCannotCompensateForRequiredFailure(t *testing.T) {
	g := newGitFixture(t)
	f := g.fixture
	a := f.addWithPolicy("a", verification.Policy{TaskChecks: []verification.CheckSpec{check("req", "exit 1", true), check("opt", "true", false)}})
	_, err := f.takeAndFinish(a.ID, app.FinishOptions{})
	wantCode(t, err, fault.CodeVerificationFailed)
	b := f.addWithPolicy("b", verification.Policy{TaskChecks: []verification.CheckSpec{check("req", "true", true), check("opt", "exit 1", false)}})
	res, err := f.takeAndFinish(b.ID, app.FinishOptions{})
	if err != nil || res.Status != task.StatusComplete || !strings.Contains(res.Summary, "optional failed: opt") {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestMissingVerifierAndTimeoutAreNotPassed(t *testing.T) {
	g := newGitFixture(t)
	f := g.fixture
	a := f.addWithPolicy("a", verification.Policy{TaskChecks: []verification.CheckSpec{{ID: "ghost", Command: []string{"no-such-verifier-binary-xyz"}, Required: true}}})
	res, err := f.takeAndFinish(a.ID, app.FinishOptions{})
	wantCode(t, err, fault.CodeVerificationFailed)
	page, _ := f.e.Evidence(f.ctx, a.ID, res.RunID)
	if page.Runs[0].Evidence[0].Outcome != verification.OutcomeError {
		t.Fatalf("missing verifier: %+v", page.Runs[0].Evidence[0])
	}
	b := f.addWithPolicy("b", verification.Policy{TaskChecks: []verification.CheckSpec{{ID: "slow", Command: sh("exec sleep 10"), Required: true, Timeout: 300 * time.Millisecond}}})
	start := time.Now()
	res, err = f.takeAndFinish(b.ID, app.FinishOptions{})
	wantCode(t, err, fault.CodeVerificationFailed)
	if time.Since(start) > 8*time.Second {
		t.Fatal("timeout not enforced promptly")
	}
	page, _ = f.e.Evidence(f.ctx, b.ID, res.RunID)
	if page.Runs[0].Evidence[0].Outcome != verification.OutcomeTimeout {
		t.Fatalf("timeout: %+v", page.Runs[0].Evidence[0])
	}
}

func TestRegressionSkippedWhenRequiredTaskCheckFails(t *testing.T) {
	g := newGitFixture(t)
	f := g.fixture
	marker := filepath.Join(t.TempDir(), "regression-ran")
	p := f.proj
	p.Regression = []verification.CheckSpec{check("full", "touch "+marker, true)}
	if err := f.e.UpdateProject(f.ctx, p); err != nil {
		t.Fatal(err)
	}
	a := f.addWithPolicy("a", verification.Policy{TaskChecks: []verification.CheckSpec{check("unit", "exit 1", true)}})
	res, err := f.takeAndFinish(a.ID, app.FinishOptions{})
	wantCode(t, err, fault.CodeVerificationFailed)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("regression must not run after a required task check failed")
	}
	page, _ := f.e.Evidence(f.ctx, a.ID, res.RunID)
	if len(page.Runs[0].Evidence) != 2 || page.Runs[0].Evidence[1].Outcome != verification.OutcomeSkipped {
		t.Fatalf("%+v", page.Runs[0].Evidence)
	}
}

func TestEvidenceReuseIsBoundToRevisionAndCheckContent(t *testing.T) {
	g := newGitFixture(t)
	f := g.fixture
	counter := filepath.Join(t.TempDir(), "count")
	script := "echo x >> " + counter
	runs := func() int {
		b, _ := os.ReadFile(counter)
		return strings.Count(string(b), "x")
	}
	pol := verification.Policy{TaskChecks: []verification.CheckSpec{check("unit", script, true)}}
	a := f.addWithPolicy("a", pol)
	res, err := f.takeAndFinish(a.ID, app.FinishOptions{})
	if err != nil || res.Status != task.StatusComplete || runs() != 1 {
		t.Fatalf("%+v %v runs=%d", res, err, runs())
	}
	// Same check content at the same revision: reused, not executed.
	b := f.addWithPolicy("b", pol)
	res, err = f.takeAndFinish(b.ID, app.FinishOptions{})
	if err != nil || res.Status != task.StatusComplete || runs() != 1 {
		t.Fatalf("reuse: %+v %v runs=%d", res, err, runs())
	}
	page, _ := f.e.Evidence(f.ctx, b.ID, res.RunID)
	if ev := page.Runs[0].Evidence[0]; !ev.Reused || ev.ReusedFrom == 0 || ev.Outcome != verification.OutcomePassed {
		t.Fatalf("expected reused evidence: %+v", ev)
	}
	// Changing the task's policy invalidates its completion; the unchanged
	// check is reused, the new one runs.
	pol2 := verification.Policy{TaskChecks: []verification.CheckSpec{check("unit", script, true), check("extra", "true", true)}}
	v, err := f.e.SetTaskPolicy(f.ctx, a.ID, pol2)
	if err != nil || v.Status != task.StatusAwaitingVerification || v.CompletedAt != nil {
		t.Fatalf("policy change: %+v %v", v, err)
	}
	vr, err := f.e.Verify(f.ctx, a.ID, app.VerifyOptions{})
	if err != nil || vr.Status != task.StatusComplete || runs() != 1 || len(vr.Evidence) != 2 || !vr.Evidence[0].Reused || vr.Evidence[1].Reused {
		t.Fatalf("re-verify: %+v %v runs=%d", vr, err, runs())
	}
	// A new revision is a new input: the check executes again.
	g.commit("two")
	c := f.addWithPolicy("c", pol)
	res, err = f.takeAndFinish(c.ID, app.FinishOptions{})
	if err != nil || runs() != 2 {
		t.Fatalf("new revision: %+v %v runs=%d", res, err, runs())
	}
	// A changed command is a new input too.
	d := f.addWithPolicy("d", verification.Policy{TaskChecks: []verification.CheckSpec{check("unit", script+" # v2", true)}})
	if _, err := f.takeAndFinish(d.ID, app.FinishOptions{}); err != nil || runs() != 3 {
		t.Fatalf("changed command: %v runs=%d", err, runs())
	}
	// Unchanged policy edit is a no-op for evidence.
	if v, err := f.e.SetTaskPolicy(f.ctx, d.ID, d.Verification); err != nil || v.Status != task.StatusComplete {
		t.Fatalf("same policy must not invalidate: %+v %v", v, err)
	}
}

func TestVerificationRefusesTreeThatDoesNotMatchSubmission(t *testing.T) {
	g := newGitFixture(t)
	f := g.fixture
	a := f.addWithPolicy("a", verification.Policy{TaskChecks: []verification.CheckSpec{check("unit", "true", true)}})
	res, err := f.takeAndFinish(a.ID, app.FinishOptions{NoVerify: true})
	if err != nil || res.Verification != "pending" {
		t.Fatalf("%+v %v", res, err)
	}
	r1 := res.Revision
	// Dirty tree: evidence would not describe r1.
	os.WriteFile(filepath.Join(g.repo, "f.txt"), []byte("dirty"), 0o644)
	vr, err := f.e.Verify(f.ctx, a.ID, app.VerifyOptions{})
	wantCode(t, err, fault.CodeVerificationFailed)
	if vr.Evidence[0].Outcome != verification.OutcomeError || !strings.Contains(vr.Evidence[0].Message, "uncommitted") {
		t.Fatalf("%+v", vr.Evidence[0])
	}
	// Different revision checked out.
	g.commit("two")
	vr, err = f.e.Verify(f.ctx, a.ID, app.VerifyOptions{})
	wantCode(t, err, fault.CodeVerificationFailed)
	if !strings.Contains(vr.Evidence[0].Message, r1) {
		t.Fatalf("%+v", vr.Evidence[0])
	}
	// Submitted revision checked out again: passes.
	g.git("checkout", "-q", r1)
	vr, err = f.e.Verify(f.ctx, a.ID, app.VerifyOptions{})
	if err != nil || vr.Status != task.StatusComplete || vr.Evidence[0].Revision != r1 {
		t.Fatalf("%+v %v", vr, err)
	}
}

func TestDirtyWorktreeSubmissionIsRejected(t *testing.T) {
	g := newGitFixture(t)
	f := g.fixture
	a := f.add("a")
	s, _ := f.e.Take(f.ctx, app.TakeRequest{Task: &a.ID})
	os.WriteFile(filepath.Join(g.repo, "new.txt"), []byte("x"), 0o644)
	_, err := f.e.Finish(f.ctx, s.Token, app.FinishOptions{})
	wantCode(t, err, fault.CodeWorkspaceDirty)
	if f.status(a.ID) != task.StatusInProgress {
		t.Fatal("a rejected submission must leave the attempt intact")
	}
	g.git("add", "new.txt")
	head := g.git("commit", "-q", "-m", "add")
	_ = head
	res, err := f.e.Finish(f.ctx, s.Token, app.FinishOptions{})
	if err != nil || res.Revision != g.git("rev-parse", "HEAD") || res.Status != task.StatusComplete {
		t.Fatalf("%+v %v", res, err)
	}
	// Replay after the tree becomes dirty again still returns the record.
	os.WriteFile(filepath.Join(g.repo, "later.txt"), []byte("x"), 0o644)
	again, err := f.e.Finish(f.ctx, s.Token, app.FinishOptions{})
	if err != nil || !again.AlreadySubmitted {
		t.Fatalf("%+v %v", again, err)
	}
}

func TestInterruptedVerificationIsRecoverable(t *testing.T) {
	g := newGitFixture(t)
	f := g.fixture
	flag := filepath.Join(t.TempDir(), "go")
	pol := verification.Policy{TaskChecks: []verification.CheckSpec{
		check("fast", "true", true),
		check("waits", "while [ ! -f "+flag+" ]; do sleep 0.05; done", true),
	}}
	a := f.addWithPolicy("a", pol)
	if _, err := f.takeAndFinish(a.ID, app.FinishOptions{NoVerify: true}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 500*time.Millisecond)
	_, err := f.e.Verify(ctx, a.ID, app.VerifyOptions{})
	cancel()
	if err == nil {
		t.Fatal("expected interruption")
	}
	if f.status(a.ID) != task.StatusAwaitingVerification {
		t.Fatalf("status: %s", f.status(a.ID))
	}
	_, err = f.e.Verify(f.ctx, a.ID, app.VerifyOptions{})
	wantCode(t, err, fault.CodeVerificationRunning)
	os.WriteFile(flag, []byte("1"), 0o644)
	vr, err := f.e.Verify(f.ctx, a.ID, app.VerifyOptions{Retry: true})
	if err != nil || vr.Status != task.StatusComplete || len(vr.Evidence) != 2 || !vr.Evidence[0].Reused || vr.Evidence[1].Reused {
		t.Fatalf("retry: %+v %v", vr, err)
	}
	page, _ := f.e.Evidence(f.ctx, a.ID, 0)
	if len(page.Runs) != 2 || page.Runs[1].Status != "error" {
		t.Fatalf("interrupted run must be closed as error: %+v", page.Runs)
	}
}

func TestVerifyGuards(t *testing.T) {
	g := newGitFixture(t)
	f := g.fixture
	a := f.addWithPolicy("a", verification.Policy{TaskChecks: []verification.CheckSpec{check("unit", "true", true)}})
	_, err := f.e.Verify(f.ctx, a.ID, app.VerifyOptions{})
	wantCode(t, err, fault.CodeNothingToVerify)
	res, err := f.takeAndFinish(a.ID, app.FinishOptions{})
	if err != nil || res.Status != task.StatusComplete {
		t.Fatalf("%+v %v", res, err)
	}
	_, err = f.e.Verify(f.ctx, a.ID, app.VerifyOptions{})
	wantCode(t, err, fault.CodeNothingToVerify)
	vr, err := f.e.Verify(f.ctx, a.ID, app.VerifyOptions{Again: true})
	if err != nil || vr.Status != task.StatusComplete || !vr.Evidence[0].Reused {
		t.Fatalf("again: %+v %v", vr, err)
	}
	// A project without a directory cannot run checks: the run errors,
	// nothing passes.
	nodir := newFixture(t)
	b := nodir.addWithPolicy("b", verification.Policy{TaskChecks: []verification.CheckSpec{check("unit", "true", true)}})
	_, err = nodir.takeAndFinish(b.ID, app.FinishOptions{})
	wantCode(t, err, fault.CodeVerificationFailed)
	page, _ := nodir.e.Evidence(nodir.ctx, b.ID, 0)
	if page.Runs[0].Evidence[0].Outcome != verification.OutcomeError {
		t.Fatalf("%+v", page.Runs[0].Evidence[0])
	}
}
