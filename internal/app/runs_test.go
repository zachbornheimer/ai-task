package app_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func (f *fixture) runResults(ref string) []map[string]any {
	f.t.Helper()
	v, err := f.e.Show(f.ctx, f.proj.ID, ref, true)
	if err != nil {
		f.t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		f.t.Fatal(err)
	}
	var out struct {
		RunResults []map[string]any `json:"run_results"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		f.t.Fatal(err)
	}
	return out.RunResults
}

func TestRunResultRecordedAndBudgeted(t *testing.T) {
	f := newGitFixture(t)
	f.proofBudget = 200 * time.Millisecond
	f.e = f.open()

	// Over budget: killed, failed, recorded with no return code.
	slow := f.addWith("slow", "sleep 3")
	s := f.claim(string(slow))
	start := time.Now()
	res, err := f.e.Verify(f.ctx, s.Token, verification.ModeProof)
	if err == nil || fault.CodeOf(err) == "" {
		t.Fatalf("slow proof must fail verification, got %v", err)
	}
	if time.Since(start) > 2500*time.Millisecond {
		t.Fatalf("overrun was not killed: %s", time.Since(start))
	}
	rr := f.runResults(string(slow))
	if len(rr) != 1 {
		t.Fatalf("want 1 RunResult, got %d: %v", len(rr), rr)
	}
	got := rr[0]
	if got["timed_out"] != true || got["return_code"] != nil || got["check_type"] != "proof" || got["budget_ms"] != float64(200) || got["passed"] != false {
		t.Fatalf("timed-out RunResult wrong: %v", got)
	}
	b, _ := json.Marshal(res)
	var shape struct {
		RunResults []map[string]any `json:"run_results"`
	}
	if err := json.Unmarshal(b, &shape); err != nil || len(shape.RunResults) != 1 || shape.RunResults[0]["timed_out"] != true {
		t.Fatalf("verify result must carry run_results: %s", b)
	}

	// Passing: one RunResult per check, proof then regression, under the
	// contract budgets.
	f.proofBudget = 0
	f.e = f.open()
	f.setRegression(check("regress", "sleep 0.05; echo reg-ok", true))
	ok := f.addWith("ok", "sleep 0.05; echo task-ok")
	f.complete(f.claim(string(ok)))
	rr = f.runResults(string(ok))
	if len(rr) != 2 {
		t.Fatalf("want 2 RunResults (proof, regression), got %d: %v", len(rr), rr)
	}
	for i, want := range []string{"proof", "regression"} {
		r := rr[i]
		if r["budget_ms"] == float64(200) || r["check_type"] != want || r["passed"] != true || r["timed_out"] == true || r["elapsed_ms"].(float64) <= 0 || r["output"] == "" || r["return_code"] != float64(0) {
			t.Fatalf("%s RunResult wrong: %v", want, r)
		}
	}
}
