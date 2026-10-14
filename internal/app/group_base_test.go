package app_test

import (
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TestGroupBaseSkipsChildrenAlreadyInTarget: a child landed by rebase (new
// SHA, its branch tip now in the target) and a child landed by plain merge
// both add nothing to the group's base, and the run says which rule held.
func TestGroupBaseSkipsChildrenAlreadyInTarget(t *testing.T) {
	f := stackedFixture(t)
	res := f.apply(
		plan.AddGroup{Key: "g", Title: "g", TaskChecks: []verification.CheckSpec{check("merged", "test -f a.txt && test -f b.txt", true)}},
		plan.AddTask{Key: "a", Title: "a", Parent: "g", TaskChecks: []verification.CheckSpec{check("unit-a", "test -d .", true)}},
		plan.AddTask{Key: "b", Title: "b", Parent: "g", TaskChecks: []verification.CheckSpec{check("unit-b", "test -d .", true)}},
	)
	g, a, b := res.Created["g"], res.Created["a"], res.Created["b"]
	revA := f.completeWith(a, "a.txt", "from a")
	f.completeWith(b, "b.txt", "from b")

	// a lands by rebase: a new commit on main, the branch moved onto it.
	// A later committer date keeps the cherry-pick from hashing to revA itself
	// when both commits land within the same second.
	t.Setenv("GIT_COMMITTER_DATE", "2030-01-01T00:00:00Z")
	f.git(f.repo, "cherry-pick", revA)
	f.git(f.repo, "update-ref", "refs/heads/atask/"+string(a), "refs/heads/main")
	// b lands by plain merge.
	f.git(f.repo, "merge", "--no-ff", "-q", "-m", "merge b", "atask/"+string(b))
	target := strings.TrimSpace(f.git(f.repo, "rev-parse", "main"))

	out, err := f.e.VerifyGroup(f.ctx, f.proj.ID, string(g))
	if err != nil || !out.Completed || !out.Passed {
		t.Fatalf("group verify: %+v %v", out, err)
	}
	if out.Revision != target {
		t.Fatalf("scratch base %s is not the target tip %s", out.Revision, target)
	}
	notes := strings.Join(out.Base, "\n")
	for _, want := range []string{string(a) + " already in target (branch_tip_in_target)", string(b) + " already in target (revision_in_target)"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("base notes missing %q:\n%s", want, notes)
		}
	}
}
