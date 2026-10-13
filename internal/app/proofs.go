package app

import (
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/proof"
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
		pol.RegressionProof = regression
	}
	return nil
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
