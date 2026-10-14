package app_test

import (
	"testing"

	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/proof"
)

// TestUpdateSizeResizesRegression: update --size alone re-sizes the task's
// existing regression, so its budget becomes 900s without re-passing it.
func TestUpdateSizeResizesRegression(t *testing.T) {
	f := newGitFixture(t)
	small := &proof.ProofSpec{Preset: "run", Subject: "test -d .", Options: map[string]string{}, Size: proof.SizeSmall}
	regression := &proof.ProofSpec{Preset: "run", Subject: "sleep 2", Options: map[string]string{}, Size: proof.SizeSmall}
	res := f.apply(plan.AddTask{Key: "r", Title: "r", Proof: small, Regression: regression})
	id := res.Created["r"]
	large := "large"
	f.apply(plan.UpdateTask{Target: plan.Ref(id), Size: &large})
	got := f.show(string(id)).Regression
	if got == nil || got.Size.Budget() != proof.SizeLarge.Budget() {
		t.Fatalf("regression after update --size large = %+v, want size large (900s)", got)
	}
}
