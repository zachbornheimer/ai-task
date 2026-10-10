package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TestPinnedPathsCannotChange: a task that pins a path cannot complete
// after changing it, the failure is the attempt's own (a strike), and
// reverting the change lets it complete. Directory pins cover their
// contents; the pin is part of the contract (`at show` carries it).
func TestPinnedPathsCannotChange(t *testing.T) {
	f := newGitFixture(t)
	os.MkdirAll(filepath.Join(f.repo, "checks"), 0o755)
	f.commit(f.repo, "checks/unit.sh", "exit 0\n")
	res := f.apply(plan.AddTask{Key: "a", Title: "a", TaskChecks: []verification.CheckSpec{{ID: "u", Command: []string{"sh", "checks/unit.sh"}, Required: true}}, Pins: []string{"checks/", "go.mod"}})
	a := res.Created["a"]
	if v := f.show(string(a)); len(v.Pins) != 2 || v.Pins[0] != "checks" {
		t.Fatalf("pins not stored or normalised: %+v", v.Pins)
	}
	s := f.claim(string(a))
	// Weakening the check script is exactly what pins prevent.
	f.commit(s.Workspace, "checks/unit.sh", "exit 0 # weakened\n")
	f.commit(s.Workspace, "a.txt", "work")
	r, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	if fault.CodeOf(err) != fault.CodeVerificationFailed || !strings.Contains(r.Summary, "checks/unit.sh") || len(r.Evidence) != 0 {
		t.Fatalf("pinned change must fail before any check runs: %v %+v", err, r)
	}
	if v := f.show(string(a)); v.Failures != 1 {
		t.Fatalf("a pinned change is the attempt's failure: failures=%d", v.Failures)
	}
	cx, err := f.e.Context(f.ctx, f.proj.ID, "", s.Token, false)
	if err != nil || !strings.Contains(cx.Prompt, "Pinned (this task may not change them") {
		t.Fatalf("context must name the pins: %v %s", err, cx.Prompt)
	}
	// Restore the pinned file and complete.
	f.commit(s.Workspace, "checks/unit.sh", "exit 0\n")
	if r, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete); err != nil || !r.Completed {
		t.Fatalf("after restoring the pin: %v %+v", err, r)
	}
	if b, _ := os.ReadFile(filepath.Join(f.repo, "checks", "unit.sh")); string(b) != "exit 0\n" {
		t.Fatal("promoted content differs")
	}
}
