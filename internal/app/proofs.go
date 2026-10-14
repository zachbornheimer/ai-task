package app

import (
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/proof"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// applyProofs records a task's proofs on its policy. A new Proof also
// replaces the derived gate so verification runs what the task shows.
func applyProofs(pol *verification.Policy, proofSpec, regression *proof.ProofSpec) error {
	if proofSpec != nil {
		gate, err := verification.ProofGate(*proofSpec)
		if err != nil {
			return err
		}
		pol.Proof, pol.TaskChecks = proofSpec, []verification.CheckSpec{gate}
	}
	if regression != nil {
		gate, err := verification.RegressionGate(*regression)
		if err != nil {
			return err
		}
		pol.RegressionProof, pol.Regression = regression, []verification.CheckSpec{gate}
	}
	return nil
}

// resizedRegression is the existing regression proof at a new size, so
// `update --size` alone re-sizes it; nil when the task has no regression.
func resizedRegression(existing *proof.ProofSpec, size string) *proof.ProofSpec {
	if existing == nil {
		return nil
	}
	resized := *existing
	resized.Size = proof.Size(size)
	return &resized
}

// applyActions replaces a task's on-complete actions. Every preset must be
// registered; the vacuity rule for proofs does not apply, since an action
// does something rather than prove something.
func applyActions(pol *verification.Policy, actions plan.Patch[proof.ActionSpec]) error {
	if !actions.Set {
		return nil
	}
	reg := proof.DefaultRegistry()
	for _, a := range actions.Value {
		if _, ok := reg.Lookup(a.Preset); !ok {
			return fault.New(fault.CodeInvalidInput, "on-complete %q: preset %q is not registered", a.Subject, a.Preset)
		}
	}
	pol.OnComplete = actions.Value
	return nil
}

// sizeBudget is the wall-clock allowance of a run of the given size: the
// proof package's contract budget, or the engine's override (tests).
func (e *Engine) sizeBudget(size proof.Size) time.Duration {
	if e.proofBudget > 0 {
		return e.proofBudget
	}
	return size.Budget()
}

// checkSize is the size class a check runs under: the size its spec carries
// (size=), small when it has none.
func checkSize(sp *proof.ProofSpec) proof.Size {
	if sp == nil || sp.Size == "" {
		return proof.SizeSmall
	}
	return sp.Size
}

// checkSizes are the size classes of a task's proof and regression runs.
func checkSizes(t task.Task) (proofSize, regressionSize proof.Size) {
	return checkSize(t.Verification.Proof), checkSize(t.Verification.RegressionProof)
}

// regressionChecks is the check a regression proof runs as; empty without one.
func regressionChecks(rp *proof.ProofSpec) []verification.CheckSpec {
	out := []verification.CheckSpec{}
	if rp == nil {
		return out
	}
	if gate, err := verification.RegressionGate(*rp); err == nil {
		out = append(out, gate)
	}
	return out
}
