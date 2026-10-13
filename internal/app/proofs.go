package app

import (
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
