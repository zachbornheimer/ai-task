package cli

import (
	"github.com/zachbornheimer/ai-task/internal/proof"
)

// proofFlags are the raw --proof and --regression values of one command.
type proofFlags struct {
	proof      string
	regression string
}

func (c *ctxt) bindProofFlags() *proofFlags {
	f := &proofFlags{}
	c.fs.StringVar(&f.proof, "proof", "", "proves the task works, \"PRESET: SUBJECT [key=value ...]\" (e.g. \"run: go test ./pkg\"), 30s")
	c.fs.StringVar(&f.regression, "regression", "", "proves nothing else broke, same grammar; --size sets its time class")
	return f
}

// parse reads both flags. Either may be empty; size, when given, becomes the
// regression's time class.
func (f *proofFlags) parse(size string) (proofSpec, regression *proof.ProofSpec, err error) {
	if f.proof != "" {
		s, err := proof.Parse(f.proof)
		if err != nil {
			return nil, nil, usage("--proof: %v", err)
		}
		proofSpec = &s
	}
	if f.regression != "" {
		s, err := proof.Parse(f.regression)
		if err != nil {
			return nil, nil, usage("--regression: %v", err)
		}
		if size != "" {
			s.Size = proof.Size(size)
		}
		regression = &s
	}
	return proofSpec, regression, nil
}

// requireBoth reports which flag is missing when a task is added.
func (f *proofFlags) requireBoth() error {
	switch {
	case f.proof == "" && f.regression == "":
		return usage("a task needs both --proof \"PRESET: SUBJECT\" and --regression \"PRESET: SUBJECT\"")
	case f.proof == "":
		return usage("a task needs --proof \"PRESET: SUBJECT\" as well as --regression")
	case f.regression == "":
		return usage("a task needs --regression \"PRESET: SUBJECT\" as well as --proof")
	}
	return nil
}
