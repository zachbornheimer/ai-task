package cli

import (
	"github.com/zachbornheimer/ai-task/internal/proof"
)

// actionFlag collects one repeatable action flag into a list shared with its
// sibling, so the actions keep the order they were given in.
type actionFlag struct {
	detach bool
	into   *[]rawAction
}

type rawAction struct {
	text   string
	detach bool
}

func (a actionFlag) String() string { return "" }
func (a actionFlag) Set(v string) error {
	*a.into = append(*a.into, rawAction{text: v, detach: a.detach})
	return nil
}

// proofFlags are the raw --proof and --regression values of one command.
type proofFlags struct {
	proof      string
	regression string
	actions    []rawAction
}

func (c *ctxt) bindProofFlags() *proofFlags {
	f := &proofFlags{}
	c.fs.StringVar(&f.proof, "proof", "", "proves the task works, \"PRESET: SUBJECT [key=value ...]\" (e.g. \"run: go test ./pkg\"), 30s")
	c.fs.StringVar(&f.regression, "regression", "", "proves nothing else broke, same grammar; size=small|medium|large|eternal sets its time class")
	c.fs.Var(actionFlag{into: &f.actions}, "on-complete", "action run after the task completes, waited on (900s), \"PRESET: SUBJECT [key=value ...]\" (repeatable)")
	c.fs.Var(actionFlag{detach: true, into: &f.actions}, "on-complete-detach", "action run after the task completes, spawned and not waited on (repeatable)")
	return f
}

// parseActions reads --on-complete and --on-complete-detach in the order given.
func (f *proofFlags) parseActions() ([]proof.ActionSpec, error) {
	var out []proof.ActionSpec
	for _, raw := range f.actions {
		a, err := proof.ParseAction(raw.text, raw.detach)
		if err != nil {
			flag := "--on-complete"
			if raw.detach {
				flag += "-detach"
			}
			return nil, usage("%s: %v", flag, err)
		}
		out = append(out, a)
	}
	return out, nil
}

// parse reads both flags. Either may be empty; each spec carries its own
// size= (a proof is small unless it says otherwise).
func (f *proofFlags) parse() (proofSpec, regression *proof.ProofSpec, err error) {
	if f.proof != "" {
		s, err := proof.ParseDefaulting(f.proof, proof.SizeSmall)
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
		regression = &s
	}
	return proofSpec, regression, nil
}

// requireBoth reports which flag is missing when a group is given proofs.
// Tasks do not use it: a task missing either proof is a draft.
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
