package verification

import (
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/proof"
)

// ProofCheckID names the gate derived from a task's Proof.
const ProofCheckID = "proof"

// ProofGate turns a task's Proof into the required check verification runs.
func ProofGate(spec proof.ProofSpec) (CheckSpec, error) {
	preset, ok := proof.DefaultRegistry().Lookup(spec.Preset)
	if !ok {
		return CheckSpec{}, fault.New(fault.CodeInvalidInput, "proof %s: preset is not registered", spec)
	}
	argv, err := preset.Argv(spec, proof.Env{})
	if err != nil {
		return CheckSpec{}, err
	}
	return CheckSpec{ID: ProofCheckID, Command: argv, Required: true}, nil
}
