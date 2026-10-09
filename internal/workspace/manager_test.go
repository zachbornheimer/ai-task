package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

type repo struct {
	t    *testing.T
	dir  string
	mgr  Manager
	ctx  context.Context
	next int
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	dir := filepath.Join(base, "repo")
	os.MkdirAll(dir, 0o755)
	r := &repo{t: t, dir: dir, ctx: context.Background()}
	r.git(dir, "init", "-q", "-b", "main")
	r.commit(dir, "f.txt", "one")
	r.mgr = Manager{Repo: dir, Root: filepath.Join(base, "ws")}
	return r
}

func (r *repo) git(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *repo) commit(dir, file, content string) string {
	r.t.Helper()
	os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644)
	r.git(dir, "add", file)
	r.next++
	r.git(dir, "commit", "-q", "-m", content)
	return r.git(dir, "rev-parse", "HEAD")
}

func TestTaskWorktreesAndSnapshots(t *testing.T) {
	r := newRepo(t)
	w1, err := r.mgr.EnsureTask(r.ctx, "at-aaaaaa", "main", 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.git(w1.Path, "rev-parse", "--abbrev-ref", "HEAD") != "at/at-aaaaaa" {
		t.Fatal("branch")
	}
	// Reuse is idempotent and reports uncommitted work left behind.
	if again, err := r.mgr.EnsureTask(r.ctx, "at-aaaaaa", "main", 0); err != nil || again != w1 {
		t.Fatalf("%+v %v", again, err)
	}
	os.WriteFile(filepath.Join(w1.Path, "wip.txt"), []byte("wip"), 0o644)
	if again, _ := r.mgr.EnsureTask(r.ctx, "at-aaaaaa", "main", 0); !again.Dirty {
		t.Fatal("dirty worktree not reported")
	}
	os.Remove(filepath.Join(w1.Path, "wip.txt"))
	rev := r.commit(w1.Path, "g.txt", "work")
	// A pruned worktree is re-attached to the existing branch.
	r.git(r.dir, "worktree", "remove", "--force", w1.Path)
	w2, err := r.mgr.EnsureTask(r.ctx, "at-aaaaaa", "main", 0)
	if err != nil || r.git(w2.Path, "rev-parse", "HEAD") != rev {
		t.Fatalf("%+v %v", w2, err)
	}
	if CurrentBranch(r.ctx, w2.Path) != "at/at-aaaaaa" || CurrentBranch(r.ctx, r.dir) != "main" || CurrentBranch(r.ctx, t.TempDir()) != "" {
		t.Fatal("current branch")
	}
	// Tokens live in the worktree's private git dir, never in the tree.
	if err := StoreToken(r.ctx, w2.Path, "sess-test"); err != nil {
		t.Fatal(err)
	}
	if LoadToken(r.ctx, w2.Path) != "sess-test" || LoadToken(r.ctx, r.dir) != "" {
		t.Fatal("token discovery")
	}
	if out := r.git(w2.Path, "status", "--porcelain"); out != "" {
		t.Fatalf("token file leaked into the tree: %s", out)
	}
	// Quarantine: the old tree moves aside detached with its edits and its
	// token; the task path gets a fresh tree on the branch.
	os.WriteFile(filepath.Join(w2.Path, "wip.txt"), []byte("wip"), 0o644)
	w3, err := r.mgr.EnsureTask(r.ctx, "at-aaaaaa", "main", 1)
	if err != nil || w3.Path != w2.Path || w3.Quarantined != w2.Path+".stale-1" || !w3.Dirty {
		t.Fatalf("quarantine: %+v %v", w3, err)
	}
	if CurrentBranch(r.ctx, w3.Quarantined) != "" || CurrentBranch(r.ctx, w3.Path) != "at/at-aaaaaa" || LoadToken(r.ctx, w3.Quarantined) != "sess-test" || LoadToken(r.ctx, w3.Path) != "" {
		t.Fatal("quarantined tree must be detached and keep its token; the new tree starts clean")
	}
	if _, err := os.Stat(filepath.Join(w3.Path, "wip.txt")); err == nil {
		t.Fatal("wip leaked")
	}
	created, err := InitRepo(r.ctx, t.TempDir())
	if err != nil || !created {
		t.Fatalf("InitRepo: %v %v", created, err)
	}
	snap, cleanup, err := r.mgr.Snapshot(r.ctx, rev)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(snap, "g.txt")); string(b) != "work" {
		t.Fatal("snapshot content")
	}
	// Editing the attempt worktree does not affect the snapshot.
	os.WriteFile(filepath.Join(w1.Path, "g.txt"), []byte("changed"), 0o644)
	if b, _ := os.ReadFile(filepath.Join(snap, "g.txt")); string(b) != "work" {
		t.Fatal("snapshot must be isolated")
	}
	cleanup()
	if _, err := os.Stat(snap); !os.IsNotExist(err) {
		t.Fatal("snapshot not removed")
	}
	if !r.mgr.BranchExists(r.ctx, w1.Branch) || r.mgr.BranchExists(r.ctx, "nope") {
		t.Fatal("BranchExists")
	}
}

func TestPrepareMergeAndPromote(t *testing.T) {
	r := newRepo(t)
	w, _ := r.mgr.EnsureTask(r.ctx, "at-bbbbbb", "main", 0)
	rev := r.commit(w.Path, "b.txt", "b")
	// Fast-forward candidate: main is checked out in the main worktree and
	// clean, so promotion fast-forwards it in place.
	cand, err := r.mgr.PrepareMerge(r.ctx, "main", []string{rev}, "promote")
	if err != nil || cand.Revision != rev {
		t.Fatalf("%+v %v", cand, err)
	}
	if err := r.mgr.Promote(r.ctx, "main", cand); err != nil {
		t.Fatal(err)
	}
	cand.Cleanup()
	if r.git(r.dir, "rev-parse", "HEAD") != rev {
		t.Fatal("main not advanced")
	}
	if _, err := os.Stat(filepath.Join(r.dir, "b.txt")); err != nil {
		t.Fatal("main worktree files not updated")
	}
	// Diverged: main moves on, a second attempt branch merges with a
	// real merge commit; the candidate is verified before the target moves.
	mainRev := r.commit(r.dir, "m.txt", "main work")
	w2, _ := r.mgr.EnsureTask(r.ctx, "at-cccccc", rev, 0)
	rev2 := r.commit(w2.Path, "c.txt", "c")
	cand2, err := r.mgr.PrepareMerge(r.ctx, "main", []string{rev2}, "promote c")
	if err != nil || cand2.Base != mainRev || cand2.Revision == rev2 || cand2.Revision == mainRev {
		t.Fatalf("%+v %v", cand2, err)
	}
	if r.git(r.dir, "rev-parse", "HEAD") != mainRev {
		t.Fatal("prepare must not move the target")
	}
	if err := r.mgr.Promote(r.ctx, "main", cand2); err != nil {
		t.Fatal(err)
	}
	cand2.Cleanup()
	if r.git(r.dir, "rev-parse", "HEAD") != cand2.Revision {
		t.Fatal("main not at merge")
	}
	// Conflict: two branches edit the same file.
	w3, _ := r.mgr.EnsureTask(r.ctx, "at-dddddd", "main", 0)
	r.commit(w3.Path, "m.txt", "conflict A")
	r.commit(r.dir, "m.txt", "conflict B")
	rev3 := r.git(w3.Path, "rev-parse", "HEAD")
	_, err = r.mgr.PrepareMerge(r.ctx, "main", []string{rev3}, "x")
	if !fault.Is(err, fault.CodeIntegrationFailed) || !strings.Contains(err.Error(), "m.txt") {
		t.Fatalf("conflict: %v", err)
	}
	// Target moved between prepare and promote: compare-and-swap refuses.
	w4, _ := r.mgr.EnsureTask(r.ctx, "at-eeeeee", "main", 0)
	rev4 := r.commit(w4.Path, "e.txt", "e")
	cand4, err := r.mgr.PrepareMerge(r.ctx, "main", []string{rev4}, "x")
	if err != nil {
		t.Fatal(err)
	}
	r.commit(r.dir, "z.txt", "moved")
	err = r.mgr.Promote(r.ctx, "main", cand4)
	cand4.Cleanup()
	if !fault.Is(err, fault.CodeIntegrationFailed) || !errors.Is(err, ErrTargetMoved) {
		t.Fatalf("moved target: %v", err)
	}
	// A target worktree with a local file where the promotion would write
	// refuses promotion (unrelated dirt is fine: TestPromoteIntoDirtyCheckoutIsSafe).
	w5, _ := r.mgr.EnsureTask(r.ctx, "at-ffffff", "main", 0)
	rev5 := r.commit(w5.Path, "f5.txt", "f5")
	cand5, _ := r.mgr.PrepareMerge(r.ctx, "main", []string{rev5}, "x")
	os.WriteFile(filepath.Join(r.dir, "f5.txt"), []byte("x"), 0o644)
	err = r.mgr.Promote(r.ctx, "main", cand5)
	cand5.Cleanup()
	if !fault.Is(err, fault.CodeIntegrationFailed) {
		t.Fatalf("dirty target: %v", err)
	}
	os.Remove(filepath.Join(r.dir, "f5.txt"))
	// Target not checked out anywhere: ref update path.
	r.git(r.dir, "checkout", "-q", "-b", "other")
	cand6, _ := r.mgr.PrepareMerge(r.ctx, "main", []string{rev5}, "x")
	if err := r.mgr.Promote(r.ctx, "main", cand6); err != nil {
		t.Fatal(err)
	}
	cand6.Cleanup()
	if r.git(r.dir, "rev-parse", "main") != cand6.Revision {
		t.Fatal("ref not updated")
	}
	if r.mgr.DefaultBranch(r.ctx) != "other" {
		t.Fatal("default branch")
	}
}

// Two promotions into a checked-out target at once: exactly one wins, the
// other reports a moved target, and the checkout is never left dirty.
func TestConcurrentPromotionsIntoCheckout(t *testing.T) {
	r := newRepo(t)
	const n = 6
	cands := make([]Candidate, n)
	for i := 0; i < n; i++ {
		w, err := r.mgr.EnsureTask(r.ctx, fmt.Sprintf("at-conc%02d", i), "main", 0)
		if err != nil {
			t.Fatal(err)
		}
		rev := r.commit(w.Path, fmt.Sprintf("c%d.txt", i), "c")
		cands[i], err = r.mgr.PrepareMerge(r.ctx, "main", []string{rev}, "x")
		if err != nil {
			t.Fatal(err)
		}
		defer cands[i].Cleanup()
	}
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range cands {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs[i] = r.mgr.Promote(r.ctx, "main", cands[i]) }(i)
	}
	wg.Wait()
	won, moved := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrTargetMoved):
			moved++
		default:
			t.Fatalf("unexpected promotion error: %v", err)
		}
	}
	if won != 1 || moved != n-1 {
		t.Fatalf("won=%d moved=%d", won, moved)
	}
	st, err := Inspect(r.ctx, r.dir)
	if err != nil || !st.Clean() {
		t.Fatalf("checkout dirty after concurrent promotion: %+v %v", st, err)
	}
	// The lock file may remain; the lock itself is the descriptor and was
	// released, so a new promoter takes it at once.
	unlock, err := r.mgr.promoteLock(r.ctx)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	unlock()
}

// A developer's checkout may be dirty: a fast-forward proceeds when it
// touches none of the dirty paths and refuses, changing nothing, when it
// would; local files are never overwritten, stashed or discarded.
func TestPromoteIntoDirtyCheckoutIsSafe(t *testing.T) {
	r := newRepo(t)
	promote := func(id, file string) (Candidate, error) {
		w, _ := r.mgr.EnsureTask(r.ctx, id, "main", 0)
		rev := r.commit(w.Path, file, "from "+id)
		cand, err := r.mgr.PrepareMerge(r.ctx, "main", []string{rev}, "x")
		if err != nil {
			t.Fatal(err)
		}
		return cand, r.mgr.Promote(r.ctx, "main", cand)
	}
	// Unrelated untracked file and unrelated tracked edit: promotion proceeds
	// and both survive.
	os.WriteFile(filepath.Join(r.dir, "notes.txt"), []byte("scratch"), 0o644)
	os.WriteFile(filepath.Join(r.dir, "g.txt"), []byte("local edit"), 0o644) // g.txt is tracked
	if _, err := promote("at-dirty1", "new1.txt"); err != nil {
		t.Fatalf("safe promotion refused: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(r.dir, "notes.txt")); string(b) != "scratch" {
		t.Fatal("untracked file lost")
	}
	if b, _ := os.ReadFile(filepath.Join(r.dir, "g.txt")); string(b) != "local edit" {
		t.Fatal("local edit lost")
	}
	if _, err := os.Stat(filepath.Join(r.dir, "new1.txt")); err != nil {
		t.Fatal("promotion did not land")
	}
	// Untracked file at a path the promotion creates: refused, untouched.
	os.WriteFile(filepath.Join(r.dir, "new2.txt"), []byte("my draft"), 0o644)
	before := r.git(r.dir, "rev-parse", "main")
	if _, err := promote("at-dirty2", "new2.txt"); err == nil || !strings.Contains(err.Error(), "new2.txt") {
		t.Fatalf("clashing untracked file not refused: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(r.dir, "new2.txt")); string(b) != "my draft" || r.git(r.dir, "rev-parse", "main") != before {
		t.Fatal("refusal was not clean")
	}
	// Tracked file with local edits that the promotion modifies: refused.
	if _, err := promote("at-dirty3", "g.txt"); err == nil || !strings.Contains(err.Error(), "g.txt") {
		t.Fatalf("clashing local edit not refused: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(r.dir, "g.txt")); string(b) != "local edit" {
		t.Fatal("local edit overwritten")
	}
	st, _ := Inspect(r.ctx, r.dir)
	if len(st.Dirty) != 3 {
		t.Fatalf("checkout changed by refused promotions: %v", st.Dirty)
	}
}
