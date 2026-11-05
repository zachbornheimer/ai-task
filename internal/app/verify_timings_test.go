package app_test

import (
	"encoding/json"
	"testing"
	"time"
)

// phaseNames are the stages of a `verify complete` the result accounts for,
// in order. A run that takes minutes after its checks passed is only
// diagnosable if each stage reports its own share.
var phaseNames = []string{"snapshot_ms", "checks_ms", "cleanup_ms", "record_ms", "prune_ms", "actions_ms"}

// TestVerifyResultReportsPhaseTimings: the result carries how long each
// stage of the completion took, and the stages add up to no more than the
// call took.
func TestVerifyResultReportsPhaseTimings(t *testing.T) {
	clearRemoteStore(t)
	f := newGitFixture(t)
	id := f.addWith("timed", "sleep 0.3")
	s := f.claim(string(id))
	start := time.Now()
	res := f.complete(s)
	wall := time.Since(start)
	if !res.Completed {
		t.Fatalf("task did not complete: %s", res.Summary)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Phases map[string]int64 `json:"phases"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, name := range phaseNames {
		ms, ok := decoded.Phases[name]
		if !ok {
			t.Fatalf("result has no %q phase; got %v", name, decoded.Phases)
		}
		if ms < 0 {
			t.Fatalf("phase %s = %dms, want >= 0", name, ms)
		}
		total += ms
	}
	if got := decoded.Phases["checks_ms"]; got < 300 {
		t.Fatalf("checks_ms = %d, want >= 300 for a check that sleeps 300ms", got)
	}
	if total > wall.Milliseconds()+5 {
		t.Fatalf("phases add up to %dms but the whole call took %dms", total, wall.Milliseconds())
	}
}
