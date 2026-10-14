package app_test

import (
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TestProofTimeoutMessageNamesFixedBudget: a timed-out proof is told its
// budget is the fixed proof budget (never the task's size) and that the
// remedy is a narrower proof or moving the long run to --regression.
func TestProofTimeoutMessageNamesFixedBudget(t *testing.T) {
	f := newGitFixture(t)
	f.setProofBudget(200 * time.Millisecond)
	a := f.addWith("slow", "sleep 2")
	medium := "medium"
	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(a), Size: &medium}}}); err != nil {
		t.Fatal(err)
	}
	s := f.claim(string(a))
	f.commit(s.Workspace, "a.txt", "a")
	r, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	if fault.CodeOf(err) != fault.CodeCheckFailed {
		t.Fatalf("want CHECK_FAILED, got %v", err)
	}
	if strings.Contains(r.Message, "medium budget") || strings.Contains(r.Message, "medium task") {
		t.Errorf("message quotes the task size: %q", r.Message)
	}
	if !strings.Contains(r.Message, "proof's size budget") {
		t.Errorf("message must name the proof size budget: %q", r.Message)
	}
	if !strings.Contains(r.Message, "narrow the proof command, set size=medium|large on the proof") {
		t.Errorf("message must name the remedy: %q", r.Message)
	}
}
