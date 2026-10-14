package app_test

import (
	"testing"

	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func TestVerifyCompleteNeedsNoCommits(t *testing.T) {
	f := newGitFixture(t)
	id := f.add("a")
	s := f.claim(string(id))

	if _, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete); err != nil {
		t.Fatalf("verify complete at the base revision: %v", err)
	}
	if v := f.show(string(id)); v.Status != task.StatusComplete {
		t.Fatalf("status = %s, want complete", v.Status)
	}
}
