package cli

import "testing"

// Cohorts were replaced by groups: the flag fails on add and update, and
// the group takes both proofs.
func TestCohortFlagIsRemoved(t *testing.T) {
	h := newProofHarness(t)
	h.failsWith([]string{"cohorts were removed; use a group"}, "add", "t", "--cohort", "x", "--proof", "run: go version", "--regression", "run: go version")
	if code, _, errOut := h.run("add", "t", "--key", "t", "--proof", "run: go version", "--regression", "run: go version"); code != ExitOK {
		t.Fatalf("add: %d %s", code, errOut)
	}
	h.failsWith([]string{"cohorts were removed; use a group"}, "update", "t", "--cohort", "x")
	h.failsWith([]string{"--regression"}, "add", "--group", "g", "--proof", "run: go version")
	if code, _, errOut := h.run("add", "--group", "g", "--key", "g", "--proof", "run: go version", "--regression", "run: go version"); code != ExitOK {
		t.Fatalf("group add: %d %s", code, errOut)
	}
}
