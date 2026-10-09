package verification

import (
	"testing"
	"time"
)

func TestPlanOrdersRequiredFirstAndRegressionLast(t *testing.T) {
	p := Policy{
		TaskChecks: []CheckSpec{{ID: "opt", Command: []string{"x"}}, {ID: "unit", Command: []string{"x"}, Required: true}},
		Regression: []CheckSpec{{ID: "full", Command: []string{"x"}, Required: true}},
	}
	steps := Plan(p)
	if len(steps) != 3 || steps[0].Check.ID != "unit" || steps[1].Check.ID != "opt" || steps[2].Check.ID != "full" || !steps[2].Regression || steps[0].Regression {
		t.Fatalf("%+v", steps)
	}
	if len(Plan(Policy{})) != 0 {
		t.Fatal("empty")
	}
}

func TestJudge(t *testing.T) {
	p := Policy{
		TaskChecks: []CheckSpec{{ID: "unit", Command: []string{"x"}, Required: true}, {ID: "lint", Command: []string{"x"}}},
		Regression: []CheckSpec{{ID: "full", Command: []string{"x"}, Required: true}},
	}
	ev := func(id string, o Outcome) Evidence { return Evidence{CheckID: id, Outcome: o} }
	cases := []struct {
		name string
		ev   []Evidence
		pass bool
	}{
		{"all pass", []Evidence{ev("unit", OutcomePassed), ev("full", OutcomePassed)}, true},
		{"optional fail ignored", []Evidence{ev("unit", OutcomePassed), ev("lint", OutcomeFailed), ev("full", OutcomePassed)}, true},
		{"required fail", []Evidence{ev("unit", OutcomeFailed), ev("lint", OutcomePassed), ev("full", OutcomePassed)}, false},
		{"missing evidence", []Evidence{ev("unit", OutcomePassed)}, false},
		{"error not passed", []Evidence{ev("unit", OutcomeError), ev("full", OutcomePassed)}, false},
		{"timeout not passed", []Evidence{ev("unit", OutcomePassed), ev("full", OutcomeTimeout)}, false},
		{"skipped not passed", []Evidence{ev("unit", OutcomeFailed), ev("full", OutcomeSkipped)}, false},
		{"nothing", nil, false},
	}
	for _, c := range cases {
		v := Judge(p, c.ev)
		if v.Passed != c.pass {
			t.Errorf("%s: passed=%v (%s)", c.name, v.Passed, v.Summary)
		}
	}
	if v := Judge(Policy{}, nil); !v.Passed {
		t.Fatal("empty policy passes vacuously")
	}
	if v := Judge(p, []Evidence{ev("unit", OutcomePassed), ev("lint", OutcomeFailed), ev("full", OutcomePassed)}); v.Summary == "" || !contains(v.Summary, "optional failed: lint") {
		t.Fatalf("summary: %s", v.Summary)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestReusable(t *testing.T) {
	c := CheckSpec{ID: "unit", Command: []string{"go", "test"}, Required: true, Timeout: time.Minute}
	prior := Evidence{CheckDigest: c.Digest(), Revision: "abc", Outcome: OutcomePassed}
	if !Reusable(prior, c, "abc") {
		t.Fatal("identical inputs must be reusable")
	}
	if Reusable(prior, c, "def") {
		t.Fatal("different revision")
	}
	if Reusable(prior, c, "") || Reusable(Evidence{CheckDigest: c.Digest(), Outcome: OutcomePassed}, c, "") {
		t.Fatal("empty revision never reusable")
	}
	c2 := c
	c2.Command = []string{"go", "test", "-race"}
	if Reusable(prior, c2, "abc") {
		t.Fatal("changed command")
	}
	c3 := c
	c3.Version = "2"
	if Reusable(prior, c3, "abc") {
		t.Fatal("changed version")
	}
	failed := prior
	failed.Outcome = OutcomeFailed
	if Reusable(failed, c, "abc") {
		t.Fatal("failures are not reused")
	}
	copied := prior
	copied.Reused = true
	if Reusable(copied, c, "abc") {
		t.Fatal("reuse must point at an original execution")
	}
}
