package app_test

import (
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

const provenOnTargetFile = "shared.txt"

// provenOnTargetGroup completes a one-child group whose child proof is
// childProof, then puts the child's change on main through a different
// commit that conflicts with the child's revision.
func provenOnTargetGroup(t *testing.T, childProof string) (*fixture, string, string) {
	t.Helper()
	f := stackedFixture(t)
	res := f.apply(
		plan.AddGroup{Key: "g", Title: "g", TaskChecks: []verification.CheckSpec{check("any", "test -d .", true)}},
		plan.AddTask{Key: "a", Title: "landed child", Parent: "g", TaskChecks: []verification.CheckSpec{check("child-proof", childProof, true)}},
	)
	g, a := res.Created["g"], res.Created["a"]
	f.completeWith(a, provenOnTargetFile, "child\n")
	f.commit(f.repo, provenOnTargetFile, "child tweaked\n")
	return f, string(g), string(a)
}

// TestGroupVerifyAcceptsChildProvenOnTarget: a completed child whose
// revision is not in the target but whose change is (it landed through a
// conflict-resolving rebase) counts as landed when its proof passes on the
// target tip, and fails the group with CHECK_FAILED naming it otherwise.
func TestGroupVerifyAcceptsChildProvenOnTarget(t *testing.T) {
	t.Run("passing proof on the target tip counts the child as landed", func(t *testing.T) {
		f, g, a := provenOnTargetGroup(t, "grep -q child "+provenOnTargetFile)
		out, err := f.e.VerifyGroup(f.ctx, f.proj.ID, g)
		if err != nil || !out.Completed || !out.Passed {
			t.Fatalf("group verify: %+v %v", out, err)
		}
		found := false
		for _, ev := range out.Evidence {
			if ev.Run != nil && ev.CheckID == "child-proof" {
				found = true
			}
		}
		if !found {
			t.Fatalf("no RunResult for %s's proof on the target tip: %+v", a, out.Evidence)
		}
	})

	t.Run("failing proof on the target tip fails the group naming the child", func(t *testing.T) {
		f, g, a := provenOnTargetGroup(t, "grep -qx child "+provenOnTargetFile)
		_, err := f.e.VerifyGroup(f.ctx, f.proj.ID, g)
		wantCode(t, err, fault.CodeCheckFailed)
		if msg := fault.MessageOf(err); !strings.Contains(msg, a) || !strings.Contains(msg, "landed child") {
			t.Fatalf("message does not name the child: %v", err)
		}
		if f.show(g).CompletedAt != nil {
			t.Fatal("group completed")
		}
	})
}
