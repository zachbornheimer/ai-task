package app_test

import (
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/proof"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func mustParseProof(t *testing.T, s string) *proof.ProofSpec {
	t.Helper()
	p, err := proof.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return &p
}

func TestInProcessPresetProofAccepted(t *testing.T) {
	f := newGitFixture(t)
	addTask := func(key, pf, reg string) string {
		t.Helper()
		res, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
			plan.AddTask{Key: key, Title: key, Proof: mustParseProof(t, pf), Regression: mustParseProof(t, reg)},
		}})
		if err != nil {
			t.Fatalf("add %q / %q: %v", pf, reg, err)
		}
		return string(res.Created[key])
	}

	id := addTask("ok", "file_exists: f.txt", "grep: f.txt pattern=^one")
	v := f.show(id)
	if v.Proof == nil || v.Proof.Preset != "file_exists" ||
		v.Regression == nil || v.Regression.Preset != "grep" {
		t.Fatalf("show must return both specs: %+v", v.Proof)
	}
	s := f.claim(id)
	res, err := f.e.Verify(f.ctx, s.Token, verification.ModeProof)
	if err != nil {
		t.Fatalf("verify proof: %v", err)
	}
	var runs []proof.RunResult
	for _, ev := range res.Evidence {
		if ev.Run != nil {
			runs = append(runs, *ev.Run)
		}
	}
	checkRuns(t, runs, "file_exists")
	f.complete(s)
	rr := f.runResults(id)
	if len(rr) < 2 {
		t.Fatalf("want proof and regression RunResults: %v", rr)
	}
	rr = rr[len(rr)-2:]
	if rr[0]["preset"] != "file_exists" || rr[1]["preset"] != "grep" ||
		rr[0]["return_code"] != float64(0) || rr[1]["return_code"] != float64(0) || rr[1]["check_type"] != "regression" {
		t.Fatalf("RunResults wrong: %v", rr)
	}

	absent := addTask("absent", "file_absent: nope.txt", "file_exists: f.txt")
	f.complete(f.claim(absent))

	bad := addTask("bad", "grep: f.txt pattern=^nomatch$", "file_exists: f.txt")
	bs := f.claim(bad)
	if _, err := f.e.Verify(f.ctx, bs.Token, verification.ModeProof); err == nil {
		t.Fatal("failing grep must fail verification")
	}
	rr = f.runResults(bad)
	if len(rr) != 1 || rr[0]["passed"] != false || rr[0]["return_code"] != float64(1) || !strings.Contains(rr[0]["output"].(string), "nomatch") {
		t.Fatalf("failing RunResult wrong: %v", rr)
	}

	pr := addTask("pr", "pr_merged: 1", "file_exists: f.txt")
	if p := f.show(pr).Proof; p == nil || p.Preset != "pr_merged" || p.Subject != "1" {
		t.Fatalf("pr_merged not stored: %+v", p)
	}
}

func checkRuns(t *testing.T, rr []proof.RunResult, preset string) {
	t.Helper()
	if len(rr) == 0 || rr[0].Preset != preset || !rr[0].Passed || rr[0].ReturnCode == nil || *rr[0].ReturnCode != 0 {
		t.Fatalf("want passing %s RunResult, got %+v", preset, rr)
	}
}
