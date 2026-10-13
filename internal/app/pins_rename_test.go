package app_test

import (
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TestPinnedFileRenamedAwayIsRefused: moving a pinned file away is a pin
// violation. Rename detection would report only the destination path, so
// the changed-path list used for pins must be rename-free.
func TestPinnedFileRenamedAwayIsRefused(t *testing.T) {
	f := newGitFixture(t)
	f.commit(f.repo, "f.txt", "pinned content that is long enough to be detected as a rename\n")
	res := f.apply(plan.AddTask{Key: "a", Title: "a", TaskChecks: okChecks(), Pins: []string{"f.txt"}})
	a := res.Created["a"]
	s := f.claim(string(a))
	f.git(s.Workspace, "mv", "f.txt", "g.txt")
	f.git(s.Workspace, "commit", "-m", "move the pinned file")
	r, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	if fault.CodeOf(err) != fault.CodeCheckFailed || !strings.Contains(r.Summary, "f.txt") || len(r.Evidence) != 0 {
		t.Fatalf("renaming a pinned file away must be refused naming it: %v %+v", err, r)
	}
}
