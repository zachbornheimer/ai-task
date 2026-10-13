package app_test

import (
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TestSpentBudgetChecksSayNotRun: when a check outruns the suite's budget,
// only that check is told it exceeded the budget; the checks behind it
// never started and say so, instead of claiming they were too slow.
func TestSpentBudgetChecksSayNotRun(t *testing.T) {
	f := newGitFixture(t)
	f.setProofBudget(300 * time.Millisecond)
	f.setRegression(check("slow", "sleep 1", true), check("after", "test -d .", true), check("last", "test -d .", false))
	a := f.add("a")
	s := f.claim(string(a))
	f.commit(s.Workspace, "a.txt", "a")

	r, _ := f.e.Verify(f.ctx, s.Token, verification.ModeRegression)
	byID := map[string]verification.Evidence{}
	for _, ev := range r.Evidence {
		byID[ev.CheckID] = ev
	}
	if len(byID) != 3 {
		t.Fatalf("evidence: %+v", r.Evidence)
	}
	if ev := byID["slow"]; ev.Outcome != verification.OutcomeTimeout || !strings.Contains(ev.Message, "exceeded the regression budget") {
		t.Fatalf("slow: %+v", ev)
	}
	for _, id := range []string{"after", "last"} {
		ev := byID[id]
		if ev.Outcome != verification.OutcomeTimeout || !strings.HasPrefix(ev.Message, "not run:") || strings.Contains(ev.Message, "exceeded") {
			t.Errorf("%s: outcome=%s message=%q, want a not-run message", id, ev.Outcome, ev.Message)
		}
	}
}
