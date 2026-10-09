package app_test

// Regression tests for findings from the adversarial review.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// Verifying an older submission must not complete a task underneath an open
// repair attempt, and a newer submission always withdraws completion.
func TestVerifyRefusesWhileAttemptOpenAndNewSubmissionWithdrawsCompletion(t *testing.T) {
	g := newGitFixture(t)
	f := g.fixture
	flag := filepath.Join(t.TempDir(), "flag")
	a := f.addWithPolicy("a", verification.Policy{TaskChecks: []verification.CheckSpec{check("unit", "test -f "+flag, true)}})
	_, err := f.takeAndFinish(a.ID, app.FinishOptions{})
	wantCode(t, err, fault.CodeVerificationFailed)
	s2, err := f.e.Take(f.ctx, app.TakeRequest{Task: &a.ID})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(flag, []byte("1"), 0o644)
	_, err = f.e.Verify(f.ctx, a.ID, app.VerifyOptions{})
	wantCode(t, err, fault.CodeTaskAlreadyTaken)
	f.c.Advance(2 * time.Hour) // attempt 2 abandoned
	_, err = f.e.Verify(f.ctx, a.ID, app.VerifyOptions{})
	wantCode(t, err, fault.CodeTaskAlreadyTaken)
	if f.status(a.ID) != task.StatusVerificationFailed {
		t.Fatalf("status %s", f.status(a.ID))
	}
	// Resume, finish: the new submission is judged on its own.
	s3, err := f.e.Take(f.ctx, app.TakeRequest{Task: &a.ID})
	if err != nil || s3.AttemptSeq != 3 {
		t.Fatalf("%+v %v", s3, err)
	}
	_ = s2
	res, err := f.e.Finish(f.ctx, s3.Token, app.FinishOptions{})
	if err != nil || res.Status != task.StatusComplete {
		t.Fatalf("%+v %v", res, err)
	}
	// Policy relaxation withdraws completion; a later tightening with a
	// failing check must leave the task not complete even though an older
	// run passed.
	v, err := f.e.SetTaskPolicy(f.ctx, a.ID, verification.Policy{TaskChecks: []verification.CheckSpec{check("unit", "test -f "+flag, true), check("never", "exit 1", true)}})
	if err != nil || v.Status != task.StatusAwaitingVerification || v.CompletedAt != nil {
		t.Fatalf("%+v %v", v, err)
	}
	_, err = f.e.Verify(f.ctx, a.ID, app.VerifyOptions{})
	wantCode(t, err, fault.CodeVerificationFailed)
	if f.status(a.ID) != task.StatusVerificationFailed {
		t.Fatalf("status %s", f.status(a.ID))
	}
}

// A policy change while an attempt is open must not hide the attempt.
func TestPolicyChangeDuringOpenAttemptKeepsAttemptVisible(t *testing.T) {
	g := newGitFixture(t)
	f := g.fixture
	a := f.addWithPolicy("a", verification.Policy{TaskChecks: []verification.CheckSpec{check("unit", "exit 1", true)}})
	_, err := f.takeAndFinish(a.ID, app.FinishOptions{})
	wantCode(t, err, fault.CodeVerificationFailed)
	s2, _ := f.e.Take(f.ctx, app.TakeRequest{Task: &a.ID})
	v, err := f.e.SetTaskPolicy(f.ctx, a.ID, verification.Policy{TaskChecks: []verification.CheckSpec{check("unit", "true", true)}})
	if err != nil || v.Status != task.StatusInProgress {
		t.Fatalf("%+v %v", v, err)
	}
	f.c.Advance(2 * time.Hour)
	if f.status(a.ID) == task.StatusAwaitingVerification {
		t.Fatal("abandoned attempt hidden behind awaiting_verification")
	}
	if _, err := f.e.Take(f.ctx, app.TakeRequest{Project: f.proj.ID}); err != nil {
		t.Fatalf("abandoned repair must be resumable: %v", err)
	}
	_ = s2
	// The new policy applies to the next submission.
	s3, _ := f.e.Take(f.ctx, app.TakeRequest{Task: &a.ID})
	_ = s3
}

// Finish reports authority problems before workspace problems.
func TestFinishReportsAuthorityBeforeDirtyTree(t *testing.T) {
	g := newGitFixture(t)
	f := g.fixture
	a := f.add("a")
	s1, _ := f.e.Take(f.ctx, app.TakeRequest{Task: &a.ID, Lease: 2 * time.Second})
	f.c.Advance(time.Hour)
	s2, _ := f.e.Take(f.ctx, app.TakeRequest{Task: &a.ID})
	os.WriteFile(filepath.Join(g.repo, "dirty.txt"), []byte("x"), 0o644)
	_, err := f.e.Finish(f.ctx, s1.Token, app.FinishOptions{})
	wantCode(t, err, fault.CodeSessionSuperseded)
	f.c.Advance(2 * time.Hour)
	_, err = f.e.Finish(f.ctx, s2.Token, app.FinishOptions{})
	wantCode(t, err, fault.CodeLeaseExpired)
}

// --no-reuse forces execution; plain --again reuses evidence.
func TestVerifyNoReuseExecutesAgain(t *testing.T) {
	g := newGitFixture(t)
	f := g.fixture
	flag := filepath.Join(t.TempDir(), "flag")
	os.WriteFile(flag, []byte("1"), 0o644)
	a := f.addWithPolicy("a", verification.Policy{TaskChecks: []verification.CheckSpec{check("unit", "test -f "+flag, true)}})
	if _, err := f.takeAndFinish(a.ID, app.FinishOptions{}); err != nil {
		t.Fatal(err)
	}
	os.Remove(flag)
	vr, err := f.e.Verify(f.ctx, a.ID, app.VerifyOptions{Again: true})
	if err != nil || !vr.Evidence[0].Reused {
		t.Fatalf("again should reuse: %+v %v", vr, err)
	}
	_, err = f.e.Verify(f.ctx, a.ID, app.VerifyOptions{Again: true, NoReuse: true})
	wantCode(t, err, fault.CodeVerificationFailed)
	if f.status(a.ID) != task.StatusVerificationFailed {
		t.Fatalf("status %s", f.status(a.ID))
	}
}

func TestTakeableListing(t *testing.T) {
	f := newFixture(t)
	a, b, c := f.add("a"), f.add("b"), f.add("c")
	s, _ := f.e.Take(f.ctx, app.TakeRequest{Task: &a.ID})
	f.c.Advance(2 * time.Hour)
	_ = b
	f.e.AddDependency(f.ctx, c.ID, b.ID)
	got, err := f.e.Takeable(f.ctx, f.proj.ID)
	if err != nil || len(got) != 2 || got[0].ID != a.ID || got[0].Status != task.StatusInterrupted || got[1].ID != b.ID {
		t.Fatalf("%+v %v", got, err)
	}
	avail, _ := f.e.Available(f.ctx, f.proj.ID)
	if len(avail) != 1 || avail[0].ID != b.ID {
		t.Fatalf("%+v", avail)
	}
	_ = s
}
