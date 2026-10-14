package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/testgit"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// completeWith claims a task, commits file=content in its worktree, and
// verifies it complete, returning the completed revision.
func (f *fixture) completeWith(id task.ID, file, content string) string {
	f.t.Helper()
	s := f.claim(string(id))
	f.commit(s.Workspace, file, content)
	res, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
	if err != nil || !res.Completed {
		f.t.Fatalf("complete %s: %+v %v", id, res, err)
	}
	return f.show(string(id)).CompletedRevision
}

// stackedFixture is a Git project whose completed work is not promoted, so
// a dependent's base is built from completed revisions alone.
func stackedFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixture(t)
}

func (f *fixture) isAncestor(dir, rev string) bool {
	f.t.Helper()
	return testgit.Command(f.t, dir, "merge-base", "--is-ancestor", rev, "HEAD").Run() == nil
}

func TestClaimBaseMergesRequirements(t *testing.T) {
	t.Run("one requirement stacks on its completed revision", func(t *testing.T) {
		f := stackedFixture(t)
		a := f.add("a")
		b := f.add("b", plan.Ref(a))
		revA := f.completeWith(a, "a.txt", "from a")
		s := f.claim(string(b))
		if _, err := os.Stat(filepath.Join(s.Workspace, "a.txt")); err != nil {
			t.Fatalf("a.txt missing in B's worktree: %v", err)
		}
		if !f.isAncestor(s.Workspace, revA) {
			t.Fatalf("A's revision %s is not an ancestor of B's HEAD", revA)
		}
	})

	t.Run("several requirements are merged", func(t *testing.T) {
		f := stackedFixture(t)
		a := f.add("a")
		b := f.add("b", plan.Ref(a))
		c := f.add("c", plan.Ref(a))
		d := f.add("d", plan.Ref(b), plan.Ref(c))
		f.completeWith(a, "a.txt", "from a")
		revB := f.completeWith(b, "b.txt", "from b")
		revC := f.completeWith(c, "c.txt", "from c")
		s := f.claim(string(d))
		for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
			if _, err := os.Stat(filepath.Join(s.Workspace, name)); err != nil {
				t.Fatalf("%s missing in D's worktree: %v", name, err)
			}
		}
		if !f.isAncestor(s.Workspace, revB) || !f.isAncestor(s.Workspace, revC) {
			t.Fatalf("B and C revisions are not both ancestors of D's HEAD")
		}
	})

	t.Run("conflicting requirements fail the claim with BASE_CONFLICT", func(t *testing.T) {
		f := stackedFixture(t)
		a := f.add("a")
		b := f.add("b", plan.Ref(a))
		c := f.add("c", plan.Ref(a))
		d := f.add("d", plan.Ref(b), plan.Ref(c))
		f.completeWith(a, "a.txt", "from a")
		revB := f.completeWith(b, "shared.txt", "line from b")
		revC := f.completeWith(c, "shared.txt", "line from c")
		_, err := f.e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID, TaskID: &d})
		wantCode(t, err, fault.CodeBaseConflict)
		v := f.show(string(d))
		if v.Status == task.StatusClaimed {
			t.Fatalf("D is claimed despite the conflict")
		}
		for _, want := range []string{"shared.txt", revB[:12], revC[:12]} {
			if !strings.Contains(v.AttentionReason, want) {
				t.Fatalf("show lacks %q: %q", want, v.AttentionReason)
			}
		}
	})
}
