package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestAttemptWorktreesAndSnapshots(t *testing.T) {
	r := newRepo(t)
	w1, err := r.mgr.CreateAttempt(r.ctx, "at-aaaaaa", 1, "main")
	if err != nil {
		t.Fatal(err)
	}
	if r.git(w1.Path, "rev-parse", "--abbrev-ref", "HEAD") != "at/at-aaaaaa/1" {
		t.Fatal("branch")
	}
	// Reuse is idempotent.
	if again, err := r.mgr.CreateAttempt(r.ctx, "at-aaaaaa", 1, "main"); err != nil || again != w1 {
		t.Fatalf("%+v %v", again, err)
	}
	rev := r.commit(w1.Path, "g.txt", "work")
	// A second attempt continues from the first attempt's branch.
	w2, err := r.mgr.CreateAttempt(r.ctx, "at-aaaaaa", 2, w1.Branch)
	if err != nil || r.git(w2.Path, "rev-parse", "HEAD") != rev {
		t.Fatalf("%+v %v", w2, err)
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
	w, _ := r.mgr.CreateAttempt(r.ctx, "at-bbbbbb", 1, "main")
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
	w2, _ := r.mgr.CreateAttempt(r.ctx, "at-cccccc", 1, rev)
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
	w3, _ := r.mgr.CreateAttempt(r.ctx, "at-dddddd", 1, "main")
	r.commit(w3.Path, "m.txt", "conflict A")
	r.commit(r.dir, "m.txt", "conflict B")
	rev3 := r.git(w3.Path, "rev-parse", "HEAD")
	_, err = r.mgr.PrepareMerge(r.ctx, "main", []string{rev3}, "x")
	if !fault.Is(err, fault.CodeIntegrationFailed) || !strings.Contains(err.Error(), "m.txt") {
		t.Fatalf("conflict: %v", err)
	}
	// Target moved between prepare and promote: compare-and-swap refuses.
	w4, _ := r.mgr.CreateAttempt(r.ctx, "at-eeeeee", 1, "main")
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
	// Dirty target worktree refuses promotion.
	w5, _ := r.mgr.CreateAttempt(r.ctx, "at-ffffff", 1, "main")
	rev5 := r.commit(w5.Path, "f5.txt", "f5")
	cand5, _ := r.mgr.PrepareMerge(r.ctx, "main", []string{rev5}, "x")
	os.WriteFile(filepath.Join(r.dir, "dirty.txt"), []byte("x"), 0o644)
	err = r.mgr.Promote(r.ctx, "main", cand5)
	cand5.Cleanup()
	if !fault.Is(err, fault.CodeIntegrationFailed) {
		t.Fatalf("dirty target: %v", err)
	}
	os.Remove(filepath.Join(r.dir, "dirty.txt"))
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
