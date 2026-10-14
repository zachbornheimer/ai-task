package workspace

import (
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// landViaRebase completes a task the way a rebasing flow does: the branch
// commit R is recorded, main moves, the branch is rebased onto main and
// main fast-forwards to the rebased tip. R is then absent from main.
func landViaRebase(r *repo, task, file string) Requirement {
	r.t.Helper()
	branch := TaskBranch(task)
	r.git(r.dir, "checkout", "-q", "-b", branch, "main")
	rev := strings.TrimSpace(r.commit(r.dir, file, "work of "+task))
	r.git(r.dir, "checkout", "-q", "main")
	r.commit(r.dir, "other-"+task+".txt", "main moved")
	r.git(r.dir, "checkout", "-q", branch)
	r.git(r.dir, "rebase", "-q", "main")
	r.git(r.dir, "checkout", "-q", "main")
	r.git(r.dir, "merge", "-q", "--ff-only", branch)
	return Requirement{Task: task, Revision: rev}
}

func TestBaseSkipsRequirementsAlreadyInTarget(t *testing.T) {
	r := newRepo(t)
	req := landViaRebase(r, "at-aaaaaa", "a.txt")
	tip := strings.TrimSpace(r.git(r.dir, "rev-parse", "main"))
	if r.mgr.BranchExists(r.ctx, TaskBranch("at-aaaaaa")) == false {
		t.Fatal("branch should still exist")
	}
	got, err := r.mgr.Base(r.ctx, "main", []Requirement{req})
	if err != nil || got != tip {
		t.Fatalf("base %q want target tip %q: %v", got, tip, err)
	}
	res, err := r.mgr.ResolveBase(r.ctx, "main", []Requirement{req})
	if err != nil || len(res.Satisfied) != 1 || res.Satisfied[0].Rule != RuleBranchTipInTarget {
		t.Fatalf("rule: %+v %v", res, err)
	}
}

func TestBaseSkipsRequirementWithDeletedBranchByPatchID(t *testing.T) {
	r := newRepo(t)
	req := landViaRebase(r, "at-bbbbbb", "b.txt")
	r.git(r.dir, "branch", "-q", "-D", TaskBranch("at-bbbbbb"))
	tip := strings.TrimSpace(r.git(r.dir, "rev-parse", "main"))
	res, err := r.mgr.ResolveBase(r.ctx, "main", []Requirement{req})
	if err != nil || res.Revision != tip || len(res.Satisfied) != 1 || res.Satisfied[0].Rule != RulePatchApplied {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestBaseFirstRequirementConflictReturnsBaseConflict(t *testing.T) {
	r := newRepo(t)
	// Two pending branches both rewrite f.txt differently from main's version.
	r.git(r.dir, "checkout", "-q", "-b", TaskBranch("at-cccccc"), "main")
	r.commit(r.dir, "f.txt", "from c")
	r.git(r.dir, "checkout", "-q", "main")
	r.commit(r.dir, "f.txt", "main edit")
	r.git(r.dir, "checkout", "-q", "-b", TaskBranch("at-dddddd"), "main")
	r.commit(r.dir, "g.txt", "from d")
	r.git(r.dir, "checkout", "-q", "main")
	c := Requirement{Task: "at-cccccc", Revision: strings.TrimSpace(r.git(r.dir, "rev-parse", TaskBranch("at-cccccc")))}
	d := Requirement{Task: "at-dddddd", Revision: strings.TrimSpace(r.git(r.dir, "rev-parse", TaskBranch("at-dddddd")))}
	_, err := r.mgr.Base(r.ctx, "main", []Requirement{c, d})
	if !fault.Is(err, fault.CodeBaseConflict) {
		t.Fatalf("want BASE_CONFLICT, got %v", err)
	}
	if !strings.Contains(err.Error(), "at-cccccc") || !strings.Contains(err.Error(), "f.txt") {
		t.Fatalf("message lacks requirement/files: %v", err)
	}
}

func TestBaseAlwaysStartsFromTargetTip(t *testing.T) {
	r := newRepo(t)
	branch := TaskBranch("at-eeeeee")
	r.git(r.dir, "checkout", "-q", "-b", branch, "main")
	aRev := strings.TrimSpace(r.commit(r.dir, "a.txt", "work of A"))
	r.git(r.dir, "checkout", "-q", "main")
	mainNew := strings.TrimSpace(r.commit(r.dir, "later.txt", "target moved after A"))
	got, err := r.mgr.Base(r.ctx, "main", []Requirement{{Task: "at-eeeeee", Revision: aRev}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{mainNew, aRev} {
		if _, err := git(r.ctx, r.dir, "merge-base", "--is-ancestor", want, got); err != nil {
			t.Fatalf("base %s lacks %s: %v", got, want, err)
		}
	}
}
