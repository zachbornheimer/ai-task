package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/fault"
)

// ErrTargetMoved reports that the target branch advanced between
// PrepareMerge and Promote; the caller rebuilds the candidate on the new
// base and verifies again.
var ErrTargetMoved = errors.New("target branch moved")

// ConflictError reports that a source could not be merged onto the target
// branch. Files lists the conflicting paths; the holder resolves them by
// merging the target into the task branch, committing, and verifying again.
type ConflictError struct {
	Source string
	Target string
	Files  []string
	Err    error
}

func (e *ConflictError) Error() string {
	msg := fmt.Sprintf("merging %s into %s failed", short(e.Source), e.Target)
	if len(e.Files) > 0 {
		msg += " (conflicts: " + strings.Join(e.Files, ", ") + ")"
	}
	return msg
}

func (e *ConflictError) Unwrap() error { return e.Err }

// Manager performs Git worktree operations for one repository. Every task
// gets one branch (`atask/<id>`) and one worktree created through
// Worktrunk, reused across attempts so uncommitted work survives a crash;
// final verification runs in a detached snapshot of the exact revision;
// promotion is a two-phase merge (prepare a candidate, then
// compare-and-swap the target) so a failing candidate never moves the
// target.
type Manager struct {
	// Repo is the main worktree root of the repository.
	Repo string
	// Root is the directory that holds snapshots and quarantined trees.
	Root string
	// Switch creates live task worktrees. Nil means WorktrunkSwitcher{}.
	Switch Switcher
}

// Info describes a task worktree.
type Info struct {
	Path   string
	Branch string
	// Dirty reports uncommitted changes left by an earlier attempt (in the
	// reused tree, or in the quarantined one).
	Dirty bool
	// Quarantined is the path the previous attempt's worktree was moved to
	// because that attempt did not end cleanly and its process may still
	// be alive. Its files are untouched; its HEAD is detached so nothing
	// it commits reaches the task branch.
	Quarantined string
}

func (m Manager) switcher() Switcher {
	if m.Switch != nil {
		return m.Switch
	}
	return WorktrunkSwitcher{}
}

// TaskBranch names the branch of a task.
func TaskBranch(taskID string) string { return "atask/" + taskID }

// EnsureTask creates the task's worktree on branch atask/<id> from base
// (a branch name or revision) via Worktrunk, or reuses it when it
// already exists.
//
// quarantineSeq is the sequence number of a previous attempt that did not
// end cleanly (lease expired, process possibly still alive), or 0. When
// set and the worktree exists, the worktree is moved aside to
// <path>.stale-<seq> with HEAD detached, and a fresh worktree on the same
// branch is created at Worktrunk's template path. The stale process keeps
// its files and its own Git directory (with its own, expired, token) and
// can no longer reach the branch or the new attempt's files.
func (m Manager) EnsureTask(ctx context.Context, taskID string, base string, quarantineSeq int) (Info, error) {
	info := Info{Branch: TaskBranch(taskID)}
	if err := os.MkdirAll(m.Root, 0o750); err != nil {
		return info, fault.Wrap(err, fault.CodeNotReady, "create workspace root")
	}
	// `git worktree add` writes a worktree's metadata non-atomically, so a
	// concurrent add or list from another claimer can read it half-written
	// and fail. Creation is serialized across processes.
	unlock, err := m.repoLock(ctx, worktreeLockFile, "worktree")
	if err != nil {
		return info, err
	}
	defer unlock()
	existing, err := m.worktreeFor(ctx, info.Branch)
	if err != nil {
		return info, err
	}
	if existing != "" {
		info.Path = existing
		if st, err := Inspect(ctx, info.Path); err == nil {
			info.Dirty = !st.Clean()
		}
		if quarantineSeq == 0 {
			return info, nil
		}
		stale := QuarantinedPath(info.Path, quarantineSeq)
		if err := gitRetry(ctx, m.Repo, "worktree", "move", info.Path, stale); err != nil {
			return info, fault.Wrap(err, fault.CodeNotReady, "quarantine worktree of attempt %d", quarantineSeq)
		}
		if _, err := git(ctx, stale, "checkout", "-q", "--detach"); err != nil {
			return info, fault.Wrap(err, fault.CodeNotReady, "detach quarantined worktree %s", stale)
		}
		info.Quarantined = stale
		info.Path = ""
	}
	create := !m.BranchExists(ctx, info.Branch)
	path, err := m.switcher().Ensure(ctx, m.Repo, info.Branch, base, create)
	if err != nil {
		return info, err
	}
	info.Path = path
	if head := CurrentBranch(ctx, info.Path); head != info.Branch {
		return info, fault.New(fault.CodeNotReady, "%s exists but is not the worktree for %s (HEAD is %q)", info.Path, info.Branch, head)
	}
	if !info.Dirty {
		if st, err := Inspect(ctx, info.Path); err == nil {
			info.Dirty = !st.Clean()
		}
	}
	return info, nil
}

// BranchExists reports whether a local branch exists.
func (m Manager) BranchExists(ctx context.Context, branch string) bool {
	_, err := git(ctx, m.Repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// Prune removes a task's worktrees: every quarantined `<path>.stale-<n>`
// directory, and the live worktree too unless keepLive. Branches are
// kept. It is best-effort and reports the directories removed.
func (m Manager) Prune(ctx context.Context, taskID string, keepLive bool) ([]string, error) {
	live, err := m.worktreeFor(ctx, TaskBranch(taskID))
	if err != nil {
		return nil, err
	}
	if live == "" {
		live = filepath.Join(m.Root, taskID)
	}
	matches, _ := filepath.Glob(live + ".stale-*")
	if !keepLive {
		if _, err := os.Stat(live); err == nil {
			matches = append(matches, live)
		}
	}
	var removed []string
	for _, path := range matches {
		_, _ = git(ctx, m.Repo, "worktree", "remove", "--force", path)
		if err := os.RemoveAll(path); err != nil {
			return removed, fault.Wrap(err, fault.CodeNotReady, "remove %s", path)
		}
		// The checks' private TMPDIR beside the worktree goes with it.
		_ = os.RemoveAll(path + ".tmp")
		removed = append(removed, path)
	}
	if !keepLive {
		_ = os.RemoveAll(live + ".tmp")
	}
	_, _ = git(ctx, m.Repo, "worktree", "prune")
	return removed, nil
}

// Contains reports whether revision is an ancestor of (or equal to) the
// tip of branch, i.e. whether a promotion of revision onto branch has
// happened.
func (m Manager) Contains(ctx context.Context, branch, revision string) (bool, error) {
	_, err := git(ctx, m.Repo, "merge-base", "--is-ancestor", revision, "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "exit status 1") || err.Error() == "" {
		return false, nil
	}
	// git prints nothing on a plain "no" (exit 1); any message is a real error.
	return false, fault.Wrap(err, fault.CodeNotReady, "compare %s with %s", short(revision), branch)
}

// Revision resolves a branch or revision to a full commit hash.
func (m Manager) Revision(ctx context.Context, ref string) (string, error) {
	out, err := git(ctx, m.Repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", fault.Wrap(err, fault.CodeNotReady, "resolve %s", ref)
	}
	return strings.TrimSpace(out), nil
}

// Snapshot checks out revision into a detached, disposable worktree so
// checks run against immutable inputs. The caller must call cleanup.
func (m Manager) Snapshot(ctx context.Context, revision string) (path string, cleanup func(), err error) {
	if err := os.MkdirAll(filepath.Join(m.Root, "snapshots"), 0o750); err != nil {
		return "", nil, fault.Wrap(err, fault.CodeNotReady, "create snapshot root")
	}
	path, err = os.MkdirTemp(filepath.Join(m.Root, "snapshots"), "snap-")
	if err != nil {
		return "", nil, fault.Wrap(err, fault.CodeNotReady, "create snapshot dir")
	}
	// git worktree add wants to create the path itself.
	if err := os.Remove(path); err != nil {
		return "", nil, fault.Wrap(err, fault.CodeNotReady, "clear snapshot dir %s", path)
	}
	if err := gitRetry(ctx, m.Repo, "worktree", "add", "--detach", path, revision); err != nil {
		return "", nil, fault.Wrap(err, fault.CodeNotReady, "snapshot %s", revision)
	}
	cleanup = func() {
		_, _ = git(context.Background(), m.Repo, "worktree", "remove", "--force", path)
		_ = os.RemoveAll(path)
		_ = os.RemoveAll(path + ".tmp")
	}
	return path, cleanup, nil
}

// Candidate is a prepared integration result that has not been promoted.
type Candidate struct {
	Base     string // target revision the candidate was built on
	Revision string // merge result (equals the last source for fast-forwards)
	cleanup  func()
}

// Cleanup removes the candidate's scratch worktree.
func (c Candidate) Cleanup() {
	if c.cleanup != nil {
		c.cleanup()
	}
}

// PrepareMerge merges sources, in order, onto the target branch's current
// revision in a scratch worktree, without touching the target. A conflict
// fails with BASE_CONFLICT naming the conflicting source; a merge git
// refuses without conflicted paths is an internal error carrying git's
// message. User commit hooks never run.
func (m Manager) PrepareMerge(ctx context.Context, target string, sources []string, message string) (Candidate, error) {
	base, err := m.Revision(ctx, "refs/heads/"+target)
	if err != nil {
		return Candidate{}, err
	}
	cand := Candidate{Base: base, Revision: base}
	// Fast-forward shortcut: a single source that already contains base.
	if len(sources) == 1 {
		if _, err := git(ctx, m.Repo, "merge-base", "--is-ancestor", base, sources[0]); err == nil {
			cand.Revision = sources[0]
			return cand, nil
		}
	}
	// The merge commit is recorded by whoever runs the verification, as
	// git merge would: a missing identity is a setup problem, not a conflict.
	if _, err := Identity(ctx, m.Repo); err != nil {
		return Candidate{}, err
	}
	path, cleanup, err := m.Snapshot(ctx, base)
	if err != nil {
		return Candidate{}, err
	}
	cand.cleanup = cleanup
	for _, src := range sources {
		conflicts, err := scratchMerge(ctx, path, message, src, "--no-ff")
		if err != nil {
			cleanup()
			if strings.TrimSpace(conflicts) == "" {
				return Candidate{}, err
			}
			ce := &ConflictError{Source: src, Target: target, Err: err}
			for f := range strings.SplitSeq(strings.TrimSpace(conflicts), "\n") {
				if f != "" {
					ce.Files = append(ce.Files, f)
				}
			}
			return Candidate{}, fault.Wrap(ce, fault.CodeBaseConflict, "%s", ce.Error())
		}
	}
	rev, err := git(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		cleanup()
		return Candidate{}, fault.Wrap(err, fault.CodeNotReady, "read candidate revision")
	}
	cand.Revision = strings.TrimSpace(rev)
	return cand, nil
}

// Promote advances the target branch from candidate.Base to
// candidate.Revision. If another promotion moved the target first, it
// fails with BASE_CONFLICT wrapping ErrTargetMoved and nothing
// changes. When the target is checked out in a worktree, that worktree
// must be clean and is fast-forwarded in place; otherwise the ref is
// updated with a compare-and-swap.
func (m Manager) Promote(ctx context.Context, target string, cand Candidate) error {
	if cand.Revision == cand.Base {
		return nil
	}
	wt, err := m.worktreeFor(ctx, target)
	if err != nil {
		return err
	}
	if wt != "" {
		// A fast-forward of a checked-out branch updates the index and
		// working tree before the ref; two at once leave the loser's
		// files ahead of HEAD. Serialize promotions into a checkout.
		unlock, err := m.promoteLock(ctx)
		if err != nil {
			return err
		}
		defer unlock()
		st, err := Inspect(ctx, wt)
		if err != nil {
			return err
		}
		if st.Revision != cand.Base {
			return fault.Wrap(ErrTargetMoved, fault.CodeBaseConflict, "target branch %s moved from %s to %s during verification", target, short(cand.Base), short(st.Revision))
		}
		// A developer's checkout may carry unrelated edits and untracked
		// files; only paths the fast-forward would touch are an obstacle,
		// and those are never overwritten, stashed or discarded.
		if !st.Clean() {
			changed, err := git(ctx, m.Repo, "diff", "--name-only", cand.Base, cand.Revision)
			if err != nil {
				return fault.Wrap(err, fault.CodeNotReady, "diff %s..%s", short(cand.Base), short(cand.Revision))
			}
			if clash := localClashes(st.Dirty, strings.Split(strings.TrimSpace(changed), "\n")); len(clash) > 0 {
				return fault.New(fault.CodeBaseConflict, "target branch %s is checked out in %s and the promotion would touch files with local changes (%s); commit, stash or move them, then verify again", target, wt, strings.Join(clash, ", "))
			}
		}
		// Git's own guard is the second net: a fast-forward never
		// overwrites local changes or untracked files.
		if _, err := git(ctx, wt, "merge", "--ff-only", cand.Revision); err != nil {
			return fault.Wrap(err, fault.CodeBaseConflict, "fast-forward %s in %s", target, wt)
		}
		return nil
	}
	if _, err := git(ctx, m.Repo, "update-ref", "refs/heads/"+target, cand.Revision, cand.Base); err != nil {
		return fault.Wrap(ErrTargetMoved, fault.CodeBaseConflict, "target branch %s moved during verification (%v)", target, err)
	}
	return nil
}

// localClashes returns the dirty paths (porcelain status lines, untracked
// included) that a change touching `changed` paths would collide with: the
// same file, or a dirty path inside a changed directory and vice versa.
func localClashes(dirty []string, changed []string) []string {
	var out []string
	for _, line := range dirty {
		if len(line) < 4 {
			continue
		}
		p := line[3:]
		if i := strings.LastIndex(p, " -> "); i >= 0 {
			p = p[i+4:]
		}
		p = strings.TrimSuffix(strings.Trim(p, `"`), "/")
		for _, c := range changed {
			if c == "" {
				continue
			}
			if c == p || strings.HasPrefix(c, p+"/") || strings.HasPrefix(p, c+"/") {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// promoteLock takes the repository-wide promotion lock: an advisory lock
// on <git-common-dir>/at-promote.lock held by this process's file
// descriptor. Ownership is the descriptor, so unlock can only release the
// instance this process holds, and the kernel releases it when the holder
// dies: there is no stale-lock detection and nothing to steal. It waits
// (bounded by ctx) while another promoter holds it.
func (m Manager) promoteLock(ctx context.Context) (func(), error) {
	return m.repoLock(ctx, promoteLockFile, "promotion")
}

// Lock files under the git common dir. Each serializes one kind of
// repository-wide mutation across processes.
const (
	promoteLockFile  = "at-promote.lock"
	worktreeLockFile = "at-worktree.lock"
)

// repoLock takes the advisory lock named file under the git common dir;
// what only labels errors.
func (m Manager) repoLock(ctx context.Context, file, what string) (func(), error) {
	common, err := git(ctx, m.Repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, fault.Wrap(err, fault.CodeNotReady, "locate git directory")
	}
	path := filepath.Join(strings.TrimSpace(common), file)
	f, err := os.OpenFile(filepath.Clean(path), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fault.Wrap(err, fault.CodeNotReady, "open %s lock", what)
	}
	delay := 10 * time.Millisecond
	for {
		locked, err := tryLockFile(f)
		if err != nil {
			discard.CloseReadOnly(f)
			return nil, fault.Wrap(err, fault.CodeNotReady, "take %s lock", what)
		}
		if locked {
			// The pid is informational only; a failed write must not lose the lock.
			discard.Writef(f, "%d\n", os.Getpid())
			return func() { unlockFile(f); discard.CloseReadOnly(f) }, nil
		}
		select {
		case <-ctx.Done():
			discard.CloseReadOnly(f)
			return nil, fault.Wrap(ctx.Err(), fault.CodeBaseConflict, "waiting for the %s lock %s", what, path)
		case <-time.After(backoffWait(delay)):
		}
		if delay < 200*time.Millisecond {
			delay *= 2
		}
	}
}

// worktreeFor returns the path of the worktree that has branch checked
// out, or "".
func (m Manager) worktreeFor(ctx context.Context, branch string) (string, error) {
	out, err := git(ctx, m.Repo, "worktree", "list", "--porcelain")
	if err != nil {
		return "", fault.Wrap(err, fault.CodeNotReady, "list worktrees")
	}
	var path string
	for line := range strings.SplitSeq(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
			if resolved, err := filepath.EvalSymlinks(path); err == nil {
				path = resolved
			}
		case line == "branch refs/heads/"+branch:
			return path, nil
		}
	}
	return "", nil
}

// DefaultBranch returns the branch HEAD points at in the main worktree, or
// "main" when detached.
func (m Manager) DefaultBranch(ctx context.Context) string {
	out, err := git(ctx, m.Repo, "symbolic-ref", "--short", "HEAD")
	if err != nil || strings.TrimSpace(out) == "" {
		return "main"
	}
	return strings.TrimSpace(out)
}

// InitRepo turns dir into a Git repository with an initial commit when it
// is not one yet, so every project is a Git project. It returns true when
// it created the repository.
func InitRepo(ctx context.Context, dir string) (bool, error) {
	if IsGitRepo(dir) {
		if _, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
			return false, nil
		}
		// Repository without commits: give it a root so branches can start.
		if err := rootCommit(ctx, dir); err != nil {
			return false, err
		}
		return false, nil
	}
	if _, err := git(ctx, dir, "init", "-q", "-b", "main"); err != nil {
		if _, err2 := git(ctx, dir, "init", "-q"); err2 != nil {
			return false, fault.Wrap(err, fault.CodeNotReady, "git init in %s", dir)
		}
	}
	if err := rootCommit(ctx, dir); err != nil {
		return false, err
	}
	return true, nil
}

// rootCommit records the empty root commit a new project starts from, as
// the repository's Git identity; only when Git has none does it fall back
// to "at", so creating a project never fails for want of one.
func rootCommit(ctx context.Context, dir string) error {
	args := []string{"commit", "-q", "--allow-empty", "-m", "chore: initialize repository"}
	if _, err := Identity(ctx, dir); err != nil {
		args = append([]string{"-c", "user.name=at", "-c", "user.email=at@localhost"}, args...)
	}
	if _, err := git(ctx, dir, args...); err != nil {
		return fault.Wrap(err, fault.CodeNotReady, "create initial commit in %s", dir)
	}
	return nil
}

// UncommittedFiles lists the paths in dir's work tree that differ from
// HEAD or are untracked (ignored files excluded), in Git's order. Untracked
// directories are listed once, with a trailing slash.
func UncommittedFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := git(ctx, dir, "status", "--porcelain", "-z", "--untracked-files=normal")
	if err != nil {
		return nil, err
	}
	var files []string
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		files = append(files, e[3:])
		// A rename or copy carries its source as the next entry.
		if e[0] == 'R' || e[0] == 'C' || e[1] == 'R' || e[1] == 'C' {
			i++
		}
	}
	return files, nil
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

// QuarantineSuffix is appended, with the attempt number, to the path of a
// worktree moved aside by a takeover.
const QuarantineSuffix = ".stale-"

// QuarantinedPath is where a takeover moves the worktree of attempt seq.
func QuarantinedPath(worktree string, seq int) string {
	return fmt.Sprintf("%s%s%d", worktree, QuarantineSuffix, seq)
}

// StaleDivergence returns the task branch tip and the HEAD of a quarantined
// worktree. They differ exactly when the stale attempt committed after the
// takeover.
func (m Manager) StaleDivergence(ctx context.Context, taskID, stalePath string) (branchTip, staleHead string, err error) {
	if branchTip, err = m.Revision(ctx, "refs/heads/"+TaskBranch(taskID)); err != nil {
		return "", "", err
	}
	out, err := git(ctx, stalePath, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", "", fault.Wrap(err, fault.CodeNotReady, "read HEAD of quarantined worktree %s", stalePath)
	}
	return branchTip, strings.TrimSpace(out), nil
}
