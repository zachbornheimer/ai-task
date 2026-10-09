package verification

// Step is one planned check and the group it belongs to.
type Step struct {
	Check CheckSpec
	// Regression steps run only if every required task check passed; a
	// failing implementation gets no expensive project-wide run.
	Regression bool
}

// Plan orders the checks of a policy. Within each group, required checks
// run before optional ones and declaration order is otherwise preserved,
// so the first failure an agent sees is the one that matters. Mandatory
// checks are never dropped.
func Plan(p Policy) []Step {
	var steps []Step
	add := func(group []CheckSpec, regression bool) {
		for _, c := range group {
			if c.Required {
				steps = append(steps, Step{Check: c, Regression: regression})
			}
		}
		for _, c := range group {
			if !c.Required {
				steps = append(steps, Step{Check: c, Regression: regression})
			}
		}
	}
	add(p.TaskChecks, false)
	add(p.Regression, true)
	return steps
}
