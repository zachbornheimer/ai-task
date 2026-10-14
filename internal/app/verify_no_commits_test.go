package app_test

import (
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

const noCommitsMessage = "no commits since claim; commit the work first (or release the claim)"

func TestVerifyCompleteRefusesNoCommits(t *testing.T) {
	f := newGitFixture(t)
	id := f.add("a")
	s := f.claim(string(id))

	_, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	wantCode(t, err, fault.CodeNotReady)
	if !strings.Contains(err.Error(), noCommitsMessage) {
		t.Fatalf("message %q lacks %q", err.Error(), noCommitsMessage)
	}
	v := f.show(string(id))
	if v.Status == task.StatusComplete {
		t.Fatal("task completed with no commits")
	}
	if v.Failures != 0 {
		t.Fatalf("refusal consumed attempt budget: %d failures", v.Failures)
	}

	f.commit(s.Workspace, "a.txt", "work")
	_, err = f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	if err != nil && fault.CodeOf(err) == fault.CodeNotReady && strings.Contains(err.Error(), noCommitsMessage) {
		t.Fatalf("still refused after a commit: %v", err)
	}
}
