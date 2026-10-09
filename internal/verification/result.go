package verification

import (
	"fmt"
	"strings"
)

// Verdict is the decision for one run.
type Verdict struct {
	Passed  bool
	Summary string
}

// Judge decides whether a run passed from the policy it executed and the
// evidence it produced. A run passes iff the policy has at least one
// required check and every required check has evidence with
// OutcomePassed. Optional checks never change the decision; a required
// check with no evidence (never run, skipped, or missing verifier) fails
// the run, and a policy with no required check proves nothing and fails.
func Judge(p Policy, evidence []Evidence) Verdict {
	if len(p.RequiredChecks()) == 0 {
		return Verdict{Passed: false, Summary: "no required checks: nothing can be proven (optional checks are informational)"}
	}
	byID := map[string]Evidence{}
	for _, e := range evidence {
		byID[e.CheckID] = e
	}
	var failed, missing, optionalFailed []string
	passed := 0
	for _, c := range p.RequiredChecks() {
		e, ok := byID[c.ID]
		switch {
		case !ok:
			missing = append(missing, c.ID)
		case e.Outcome == OutcomePassed:
			passed++
		default:
			failed = append(failed, fmt.Sprintf("%s (%s)", c.ID, e.Outcome))
		}
	}
	for _, group := range [][]CheckSpec{p.TaskChecks, p.Regression} {
		for _, c := range group {
			if c.Required {
				continue
			}
			if e, ok := byID[c.ID]; ok && e.Outcome != OutcomePassed && e.Outcome != OutcomeSkipped {
				optionalFailed = append(optionalFailed, c.ID)
			}
		}
	}
	v := Verdict{Passed: len(failed) == 0 && len(missing) == 0}
	var parts []string
	parts = append(parts, fmt.Sprintf("%d/%d required checks passed", passed, len(p.RequiredChecks())))
	if len(failed) > 0 {
		parts = append(parts, "failed: "+strings.Join(failed, ", "))
	}
	if len(missing) > 0 {
		parts = append(parts, "no evidence: "+strings.Join(missing, ", "))
	}
	if len(optionalFailed) > 0 {
		parts = append(parts, "optional failed: "+strings.Join(optionalFailed, ", "))
	}
	v.Summary = strings.Join(parts, "; ")
	return v
}
