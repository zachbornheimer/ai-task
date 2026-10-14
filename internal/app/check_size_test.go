package app_test

import (
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/proof"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TestCheckSizeSetsRunBudget: a check's own size= is the budget its run gets
// at verify: a medium proof 300s, a large regression 900s, an unsized
// regression the small 30s.
func TestCheckSizeSetsRunBudget(t *testing.T) {
	f := newGitFixture(t)
	proofSpec := &proof.ProofSpec{Preset: "run", Subject: "test -d .", Options: map[string]string{}, Size: proof.SizeMedium}
	regression := &proof.ProofSpec{Preset: "run", Subject: "test -d .", Options: map[string]string{}, Size: proof.SizeLarge}
	id := f.apply(plan.AddTask{Key: "sized", Title: "sized", Proof: proofSpec, Regression: regression}).Created["sized"]
	s := f.claim(string(id))
	f.commit(s.Workspace, "sized.txt", "x")
	res, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	if err != nil || !res.Completed {
		t.Fatalf("verify: %+v %v", res, err)
	}
	budgets := map[proof.CheckType]time.Duration{}
	for i := range res.Evidence {
		if run := res.Evidence[i].Run; run != nil {
			budgets[run.CheckType] = run.Budget()
		}
	}
	// The suite allowance trims a check's budget by the time already spent.
	for ct, want := range map[proof.CheckType]time.Duration{proof.CheckProof: 300 * time.Second, proof.CheckRegression: 900 * time.Second} {
		if got := budgets[ct]; got > want || got < want-5*time.Second {
			t.Fatalf("%s budget = %s, want about %s", ct, got, want)
		}
	}
}
