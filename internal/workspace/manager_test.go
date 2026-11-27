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
	"github.com/zachbornheimer/ai-task/internal/must"
	"github.com/zachbornheimer/ai-task/internal/testgit"
)

type repo struct {
	t   *testing.T
	dir string
	mgr Manager
	ctx context.Context
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("HK", "0")
	base := t.TempDir()
	dir := filepath.Join(base, "repo")
	must.OK(t, os.MkdirAll(dir, 0o750))
	r := &repo{t: t, dir: dir, ctx: context.Background()}
	r.git(dir, "init", "-q", "-b", "main")
	r.commit(dir, "f.txt", "one")
	ws := filepath.Join(base, "ws")
	r.mgr = Manager{Repo: dir, Root: ws, Switch: gitSwitcher{t: t, repo: dir, root: ws}}
	return r
}

// gitSwitcher stands in for Worktrunk in unit tests: same Ensure contract,
// paths under root using git worktree add.
type gitSwitcher struct {
	t    *testing.T
	repo string
	root string
}

func (g gitSwitcher) Ensure(ctx context.Context, _, branch, base string, create bool) (string, error) {
	g.t.Helper()
	path := filepath.Join(g.root, strings.ReplaceAll(branch, "/", "-"))
	if resolved, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		path = filepath.Join(resolved, filepath.Base(path))
	}
	if create {
		cmd := testgit.CommandContext(ctx, g.t, g.repo, "branch", branch, base)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("branch: %w\n%s", err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return path, nil
	}
	cmd := testgit.CommandContext(ctx, g.t, g.repo, "worktree", "add", path, branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("worktree add: %w\n%s", err, out)
	}
	return path, nil
}

func (r *repo) git(dir string, args ...string) string {
	r.t.Helper()
	return testgit.Run(r.t, dir, args...)
}

func (r *repo) commit(dir, file, content string) string {
	r.t.Helper()
	must.OK(r.t, os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600))
	r.git(dir, "add", file)
	r.git(dir, "commit", "-q", "-m", content)
	return r.git(dir, "rev-parse", "HEAD")
}

func TestTaskWorktreesAndSnapshots(t *testing.T) {
	r := newRepo(t)
	w1, err := r.mgr.EnsureTask(r.ctx, "at-aaaaaa", "main", 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.git(w1.Path, "rev-parse", "--abbrev-ref", "HEAD") != "atask/at-aaaaaa" {
		t.Fatal("branch")
	}
	// Reuse is idempotent and reports uncommitted work left behind.
	if again, err := r.mgr.EnsureTask(r.ctx, "at-aaaaaa", "main", 0); err != nil || again != w1 {
		t.Fatalf("%+v %v", again, err)
	}
	must.OK(t, os.WriteFile(filepath.Join(w1.Path, "wip.txt"), []byte("wip"), 0o600))
	if again, err := r.mgr.EnsureTask(r.ctx, "at-aaaaaa", "main", 0); err != nil || !again.Dirty {
		t.Fatalf("dirty worktree: %+v, %v", again, err)
	}
	must.OK(t, os.Remove(filepath.Join(w1.Path, "wip.txt")))
	rev := r.commit(w1.Path, "g.txt", "work")
	// A pruned worktree is re-attached to the existing branch.
	r.git(r.dir, "worktree", "remove", "--force", w1.Path)
	w2, err := r.mgr.EnsureTask(r.ctx, "at-aaaaaa", "main", 0)
	if err != nil || r.git(w2.Path, "rev-parse", "HEAD") != rev {
		t.Fatalf("%+v %v", w2, err)
	}
	if CurrentBranch(r.ctx, w2.Path) != "atask/at-aaaaaa" || CurrentBranch(r.ctx, r.dir) != "main" || CurrentBranch(r.ctx, t.TempDir()) != "" {
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
	must.OK(t, os.WriteFile(filepath.Join(w2.Path, "wip.txt"), []byte("wip"), 0o600))
	w3, err := r.mgr.EnsureTask(r.ctx, "at-aaaaaa", "main", 1)
	if err != nil || w3.Path != w2.Path || w3.Quarantined != w2.Path+".stale-1" || !w3.Dirty {
		t.Fatalf("quarantine: %+v %v", w3, err)
	}
	if CurrentBranch(r.ctx, w3.Quarantined) != "" || CurrentBranch(r.ctx, w3.Path) != "atask/at-aaaaaa" || LoadToken(r.ctx, w3.Quarantined) != "sess-test" || LoadToken(r.ctx, w3.Path) != "" {
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
	t.Cleanup(cleanup)
	snapshot, err := os.OpenRoot(snap)
	must.OK(t, err)
	t.Cleanup(func() { must.OK(t, snapshot.Close()) })
	b, err := snapshot.ReadFile("g.txt")
	must.OK(t, err)
	if string(b) != "work" {
		t.Fatalf("snapshot content: got %q", b)
	}
	// Editing the attempt worktree does not affect the snapshot.
	must.OK(t, os.WriteFile(filepath.Join(w1.Path, "g.txt"), []byte("changed"), 0o600))
	b, err = snapshot.ReadFile("g.txt")
	must.OK(t, err)
	if string(b) != "work" {
		t.Fatalf("snapshot must be isolated: got %q", b)
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
	r.git(r.dir, "config", "user.name", "t")
	r.git(r.dir, "config", "user.email", "t@t")
	w, err := r.mgr.EnsureTask(r.ctx, "at-bbbbbb", "main", 0)
	must.OK(t, err)
	rev := r.commit(w.Path, "b.txt", "b")
	// Fast-forward candidate: main is checked out in the main worktree and
	// clean, so promotion fast-forwards it in place.
	cand, err := r.mgr.PrepareMerge(r.ctx, "main", []string{rev}, "promote")
	if err != nil || cand.Revision != rev {
		t.Fatalf("%+v %v", cand, err)
	}
	t.Cleanup(cand.Cleanup)
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
	w2, err := r.mgr.EnsureTask(r.ctx, "at-cccccc", rev, 0)
	must.OK(t, err)
	rev2 := r.commit(w2.Path, "c.txt", "c")
	cand2, err := r.mgr.PrepareMerge(r.ctx, "main", []string{rev2}, "promote c")
	if err != nil || cand2.Base != mainRev || cand2.Revision == rev2 || cand2.Revision == mainRev {
		t.Fatalf("%+v %v", cand2, err)
	}
	t.Cleanup(cand2.Cleanup)
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
	w3, err := r.mgr.EnsureTask(r.ctx, "at-dddddd", "main", 0)
	must.OK(t, err)
	r.commit(w3.Path, "m.txt", "conflict A")
	r.commit(r.dir, "m.txt", "conflict B")
	rev3 := r.git(w3.Path, "rev-parse", "HEAD")
	_, err = r.mgr.PrepareMerge(r.ctx, "main", []string{rev3}, "x")
	if !fault.Is(err, fault.CodeBaseConflict) || !strings.Contains(err.Error(), "m.txt") {
		t.Fatalf("conflict: %v", err)
	}
	// Target moved between prepare and promote: compare-and-swap refuses.
	w4, err := r.mgr.EnsureTask(r.ctx, "at-eeeeee", "main", 0)
	must.OK(t, err)
	rev4 := r.commit(w4.Path, "e.txt", "e")
	cand4, err := r.mgr.PrepareMerge(r.ctx, "main", []string{rev4}, "x")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cand4.Cleanup)
	r.commit(r.dir, "z.txt", "moved")
	err = r.mgr.Promote(r.ctx, "main", cand4)
	cand4.Cleanup()
	if !fault.Is(err, fault.CodeBaseConflict) || !errors.Is(err, ErrTargetMoved) {
		t.Fatalf("moved target: %v", err)
	}
	// A target worktree with a local file where the promotion would write
	// refuses promotion (unrelated dirt is fine: TestPromoteIntoDirtyCheckoutIsSafe).
	w5, err := r.mgr.EnsureTask(r.ctx, "at-ffffff", "main", 0)
	must.OK(t, err)
	rev5 := r.commit(w5.Path, "f5.txt", "f5")
	cand5, err := r.mgr.PrepareMerge(r.ctx, "main", []string{rev5}, "x")
	must.OK(t, err)
	t.Cleanup(cand5.Cleanup)
	must.OK(t, os.WriteFile(filepath.Join(r.dir, "f5.txt"), []byte("x"), 0o600))
	err = r.mgr.Promote(r.ctx, "main", cand5)
	cand5.Cleanup()
	if !fault.Is(err, fault.CodeBaseConflict) {
		t.Fatalf("dirty target: %v", err)
	}
	must.OK(t, os.Remove(filepath.Join(r.dir, "f5.txt")))
	// Target not checked out anywhere: ref update path.
	r.git(r.dir, "checkout", "-q", "-b", "other")
	cand6, err := r.mgr.PrepareMerge(r.ctx, "main", []string{rev5}, "x")
	must.OK(t, err)
	t.Cleanup(cand6.Cleanup)
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

// Competing promotions into a checked-out target: exactly one wins, the
// others report a moved target, and the checkout is never left dirty.
func TestConcurrentPromotionsIntoCheckout(t *testing.T) {
	r := newRepo(t)
	const n = 6
	cands := make([]Candidate, n)
	for i := range n {
		w, err := r.mgr.EnsureTask(r.ctx, fmt.Sprintf("at-conc%02d", i), "main", 0)
		if err != nil {
			t.Fatal(err)
		}
		rev := r.commit(w.Path, fmt.Sprintf("c%d.txt", i), "c")
		cands[i], err = r.mgr.PrepareMerge(r.ctx, "main", []string{rev}, "x")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cands[i].Cleanup)
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
	r.git(r.dir, "config", "user.name", "t")
	r.git(r.dir, "config", "user.email", "t@t")
	promote := func(id, file string) error {
		w, err := r.mgr.EnsureTask(r.ctx, id, "main", 0)
		must.OK(t, err)
		rev := r.commit(w.Path, file, "from "+id)
		cand, err := r.mgr.PrepareMerge(r.ctx, "main", []string{rev}, "x")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cand.Cleanup)
		return r.mgr.Promote(r.ctx, "main", cand)
	}
	r.commit(r.dir, "g.txt", "committed content")
	if got := r.git(r.dir, "ls-files", "--", "g.txt"); got != "g.txt" {
		t.Fatalf("tracked-file precondition: got %q", got)
	}
	// Unrelated untracked file and unrelated tracked edit: promotion proceeds
	// and both survive.
	must.OK(t, os.WriteFile(filepath.Join(r.dir, "notes.txt"), []byte("scratch"), 0o600))
	must.OK(t, os.WriteFile(filepath.Join(r.dir, "g.txt"), []byte("local edit"), 0o600)) // g.txt is tracked
	if err := promote("at-dirty1", "new1.txt"); err != nil {
		t.Fatalf("safe promotion refused: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(r.dir, "notes.txt"))
	must.OK(t, err)
	if string(b) != "scratch" {
		t.Fatalf("untracked file content: got %q", b)
	}
	b, err = os.ReadFile(filepath.Join(r.dir, "g.txt"))
	must.OK(t, err)
	if string(b) != "local edit" {
		t.Fatalf("tracked file content: got %q", b)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "new1.txt")); err != nil {
		t.Fatal("promotion did not land")
	}
	// Untracked file at a path the promotion creates: refused, untouched.
	must.OK(t, os.WriteFile(filepath.Join(r.dir, "new2.txt"), []byte("my draft"), 0o600))
	before := r.git(r.dir, "rev-parse", "main")
	if err := promote("at-dirty2", "new2.txt"); err == nil || !strings.Contains(err.Error(), "new2.txt") {
		t.Fatalf("clashing untracked file not refused: %v", err)
	}
	b, err = os.ReadFile(filepath.Join(r.dir, "new2.txt"))
	must.OK(t, err)
	if string(b) != "my draft" || r.git(r.dir, "rev-parse", "main") != before {
		t.Fatalf("refusal changed content or target: content %q", b)
	}
	// Tracked file with local edits that the promotion modifies: refused.
	if err := promote("at-dirty3", "g.txt"); err == nil || !strings.Contains(err.Error(), "g.txt") {
		t.Fatalf("clashing local edit not refused: %v", err)
	}
	b, err = os.ReadFile(filepath.Join(r.dir, "g.txt"))
	must.OK(t, err)
	if string(b) != "local edit" {
		t.Fatalf("local edit overwritten: got %q", b)
	}
	st, err := Inspect(r.ctx, r.dir)
	must.OK(t, err)
	if len(st.Dirty) != 3 {
		t.Fatalf("checkout changed by refused promotions: %v", st.Dirty)
	}
}

// isolateGitIdentity leaves repository-local identity as the only identity.
func isolateGitIdentity(t *testing.T) {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "gitconfig")
	must.OK(t, os.WriteFile(empty, nil, 0o600))
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	// Headless hosts inject command-scope config independently of files.
	// t.Setenv restores both injection forms, including signing, on cleanup.
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "")
		must.OK(t, os.Unsetenv(k))
	}
}

func TestIsolateGitIdentityInjectedConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"count", map[string]string{
			"GIT_CONFIG_COUNT":      "1",
			"GIT_CONFIG_KEY_0":      "core.abbrev",
			"GIT_CONFIG_VALUE_0":    "12",
			"GIT_CONFIG_PARAMETERS": "",
		}},
		{"parameters", map[string]string{
			"GIT_CONFIG_COUNT":      "0",
			"GIT_CONFIG_PARAMETERS": "'core.abbrev=12'",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			abbrev := func() (string, error) {
				out, err := exec.Command("git", "config", "--get", "core.abbrev").CombinedOutput()
				return strings.TrimSpace(string(out)), err
			}
			if got, err := abbrev(); err != nil || got != "12" {
				t.Fatalf("injected config before isolation = %q, %v; want 12", got, err)
			}
			t.Run("isolated", func(t *testing.T) {
				isolateGitIdentity(t)
				got, err := abbrev()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 || got != "" {
					t.Fatalf("injected config leaked through isolation: %q, %v", got, err)
				}
			})
			if got, err := abbrev(); err != nil || got != "12" {
				t.Fatalf("injected config after cleanup = %q, %v; want 12", got, err)
			}
		})
	}
}

func TestPrepareMergeWithoutIdentity(t *testing.T) {
	isolateGitIdentity(t)
	r := newRepo(t)
	r.git(r.dir, "config", "user.useConfigOnly", "true")
	r.git(r.dir, "checkout", "-q", "-b", "side")
	rev := r.commit(r.dir, "side.txt", "side")
	r.git(r.dir, "checkout", "-q", "main")
	r.commit(r.dir, "main.txt", "main")

	// A merge is needed and Git has no identity: a setup error naming the
	// fix, not a conflict.
	_, err := r.mgr.PrepareMerge(r.ctx, "main", []string{rev}, "merge side\n\nAt-Task: at-aaaaaa")
	if fault.CodeOf(err) != fault.CodeNotReady || !strings.Contains(fault.MessageOf(err), "config user.email") {
		t.Fatalf("want NOT_READY naming the fix, got %v", err)
	}
	if _, ok := errors.AsType[*ConflictError](err); ok {
		t.Fatal("missing identity reported as a conflict")
	}

	// With one, the merge is recorded as that identity.
	r.git(r.dir, "config", "user.name", "Repo Owner")
	r.git(r.dir, "config", "user.email", "owner@example.com")
	cand, err := r.mgr.PrepareMerge(r.ctx, "main", []string{rev}, "merge side\n\nAt-Task: at-aaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cand.Cleanup)
	if who := r.git(r.dir, "log", "-1", "--format=%an <%ae>|%cn <%ce>", cand.Revision); who != "Repo Owner <owner@example.com>|Repo Owner <owner@example.com>" {
		t.Fatalf("merge recorded as %q", who)
	}
	if id, err := Identity(r.ctx, r.dir); err != nil || id != "Repo Owner <owner@example.com>" {
		t.Fatalf("Identity = %q, %v", id, err)
	}
}

func TestPruneRemovesTmp(t *testing.T) {
	r := newRepo(t)
	w, err := r.mgr.EnsureTask(r.ctx, "at-bbbbbb", "main", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Checks get a private TMPDIR, named by the worktree path.
	tmp, err := EnsureCheckTmp(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	must.OK(t, os.WriteFile(filepath.Join(tmp, "at-changed-files"), []byte("x\n"), 0o600))
	if _, err := r.mgr.Prune(r.ctx, "at-bbbbbb", false); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{w.Path, tmp} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s left behind after prune", p)
		}
	}
}

func TestSnapshotCleanupRemovesTmp(t *testing.T) {
	r := newRepo(t)
	path, cleanup, err := r.mgr.Snapshot(r.ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := EnsureCheckTmp(path)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	for _, p := range []string{path, tmp} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s left behind after snapshot cleanup", p)
		}
	}
}

// Concurrent claimers each create a task worktree in the same repository.
// Git's worktree metadata is written non-atomically, so an unserialised
// `worktree list` or `worktree add` can fail mid-creation; a transient
// failure like that must never surface as a failed workspace.
func TestConcurrentEnsureTaskNeverFails(t *testing.T) {
	r := newRepo(t)
	const claimers = 10
	for round := range 2 {
		errs := make([]error, claimers)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Go(func() {
				_, errs[i] = r.mgr.EnsureTask(r.ctx, fmt.Sprintf("at-r%dc%02d", round, i), "main", 0)
			})
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("round %d claimer %d: %v", round, i, err)
			}
		}
	}
}
