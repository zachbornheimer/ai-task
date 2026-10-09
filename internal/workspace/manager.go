package workspace

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

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
// gets one branch (`at/<id>`) and one worktree, reused across attempts so
// uncommitted work survives a crash; final verification runs in a detached
// snapshot of the exact revision; promotion is a two-phase merge (prepare a
// candidate, then compare-and-swap the target) so a failing candidate never
// moves the target.
type Manager struct {
	// Repo is the main worktree root of the repository.
	Repo string
	// Root is the directory that holds task worktrees and snapshots.
	Root string
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

// TaskBranch names the branch of a task.
func TaskBranch(taskID string) string { return "at/" + taskID }

// EnsureTask creates the task's worktree on branch at/<id> from base (a
// branch name or revision) or reuses it when it already exists.
//
// quarantineSeq is the sequence number of a previous attempt that did not
// end cleanly (lease expired, process possibly still alive), or 0. When
// set and the worktree exists, the worktree is moved aside to
// <path>.stale-<seq> with HEAD detached, and a fresh worktree on the same
// branch takes its place at the task path. The stale process keeps its
// files and its own Git directory (with its own, expired, token) and can
// no longer reach the branch or the new attempt's files.
func (m Manager) EnsureTask(ctx context.Context, taskID string, base string, quarantineSeq int) (Info, error) {
	info := Info{Path: filepath.Join(m.Root, taskID), Branch: TaskBranch(taskID)}
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		return info, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "create workspace root")
	}
	if _, err := os.Stat(filepath.Join(info.Path, ".git")); err == nil {
		head, err := git(ctx, info.Path, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil || strings.TrimSpace(head) != info.Branch {
			return info, fault.New(fault.CodeWorkspaceUnavailable, "%s exists but is not the worktree for %s", info.Path, info.Branch)
		}
		if st, err := Inspect(ctx, info.Path); err == nil {
			info.Dirty = !st.Clean()
		}
		if quarantineSeq == 0 {
			return info, nil
		}
		stale := fmt.Sprintf("%s.stale-%d", info.Path, quarantineSeq)
		if _, err := gitRetry(ctx, m.Repo, "worktree", "move", info.Path, stale); err != nil {
			return info, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "quarantine worktree of attempt %d", quarantineSeq)
		}
		if _, err := git(ctx, stale, "checkout", "-q", "--detach"); err != nil {
			return info, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "detach quarantined worktree %s", stale)
		}
		info.Quarantined = stale
		// The branch is free again; attach a fresh worktree below.
	}
	// Two idempotent steps instead of `worktree add -b`: under concurrent
	// claims Git can create the branch and then fail on another
	// worktree's half-written metadata, and a retry of `-b` would then
	// refuse because the branch exists.
	if !m.BranchExists(ctx, info.Branch) {
		if _, err := gitRetry(ctx, m.Repo, "branch", info.Branch, base); err != nil && !m.BranchExists(ctx, info.Branch) {
			return info, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "create branch %s from %s", info.Branch, base)
		}
	}
	if _, err := gitRetry(ctx, m.Repo, "worktree", "add", info.Path, info.Branch); err != nil {
		// A retried add may have registered the worktree before failing.
		if CurrentBranch(ctx, info.Path) == info.Branch {
			return info, nil
		}
		// A worktree deleted outside Git leaves a stale registration that
		// pins the branch; prune only then (an unconditional prune races
		// with other processes' half-created worktrees).
		if msg := err.Error(); strings.Contains(msg, "already checked out") || strings.Contains(msg, "already registered") || strings.Contains(msg, "already used by worktree") {
			_, _ = git(ctx, m.Repo, "worktree", "prune")
			if _, err2 := gitRetry(ctx, m.Repo, "worktree", "add", info.Path, info.Branch); err2 == nil || CurrentBranch(ctx, info.Path) == info.Branch {
				return info, nil
			}
		}
		return info, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "attach worktree for %s", info.Branch)
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
	live := filepath.Join(m.Root, taskID)
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
			return removed, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "remove %s", path)
		}
		removed = append(removed, path)
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
	return false, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "compare %s with %s", short(revision), branch)
}

// Revision resolves a branch or revision to a full commit hash.
func (m Manager) Revision(ctx context.Context, ref string) (string, error) {
	out, err := git(ctx, m.Repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", fault.Wrap(err, fault.CodeWorkspaceUnavailable, "resolve %s", ref)
	}
	return strings.TrimSpace(out), nil
}

// Snapshot checks out revision into a detached, disposable worktree so
// checks run against immutable inputs. The caller must call cleanup.
func (m Manager) Snapshot(ctx context.Context, revision string) (path string, cleanup func(), err error) {
	if err := os.MkdirAll(filepath.Join(m.Root, "snapshots"), 0o755); err != nil {
		return "", nil, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "create snapshot root")
	}
	path, err = os.MkdirTemp(filepath.Join(m.Root, "snapshots"), "snap-")
	if err != nil {
		return "", nil, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "create snapshot dir")
	}
	os.Remove(path) // git worktree add wants to create it
	if _, err := gitRetry(ctx, m.Repo, "worktree", "add", "--detach", path, revision); err != nil {
		return "", nil, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "snapshot %s", revision)
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
// fails with INTEGRATION_FAILED naming the conflicting source.
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
	path, cleanup, err := m.Snapshot(ctx, base)
	if err != nil {
		return Candidate{}, err
	}
	cand.cleanup = cleanup
	for _, src := range sources {
		if _, err := git(ctx, path, "-c", "user.name=at", "-c", "user.email=at@localhost", "merge", "--no-ff", "--no-edit", "-m", message, src); err != nil {
			conflicts, _ := git(ctx, path, "diff", "--name-only", "--diff-filter=U")
			_, _ = git(ctx, path, "merge", "--abort")
			cleanup()
			ce := &ConflictError{Source: src, Target: target, Err: err}
			for _, f := range strings.Split(strings.TrimSpace(conflicts), "\n") {
				if f != "" {
					ce.Files = append(ce.Files, f)
				}
			}
			return Candidate{}, fault.Wrap(ce, fault.CodeIntegrationFailed, "%s", ce.Error())
		}
	}
	rev, err := git(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		cleanup()
		return Candidate{}, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "read candidate revision")
	}
	cand.Revision = strings.TrimSpace(rev)
	return cand, nil
}

// Promote advances the target branch from candidate.Base to
// candidate.Revision. If another promotion moved the target first, it
// fails with INTEGRATION_FAILED wrapping ErrTargetMoved and nothing
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
			return fault.Wrap(ErrTargetMoved, fault.CodeIntegrationFailed, "target branch %s moved from %s to %s during verification", target, short(cand.Base), short(st.Revision))
		}
		// A developer's checkout may carry unrelated edits and untracked
		// files; only paths the fast-forward would touch are an obstacle,
		// and those are never overwritten, stashed or discarded.
		if !st.Clean() {
			changed, err := git(ctx, m.Repo, "diff", "--name-only", cand.Base, cand.Revision)
			if err != nil {
				return fault.Wrap(err, fault.CodeWorkspaceUnavailable, "diff %s..%s", short(cand.Base), short(cand.Revision))
			}
			if clash := localClashes(st.Dirty, strings.Split(strings.TrimSpace(changed), "\n")); len(clash) > 0 {
				return fault.New(fault.CodeIntegrationFailed, "target branch %s is checked out in %s and the promotion would touch files with local changes (%s); commit, stash or move them, then verify again", target, wt, strings.Join(clash, ", "))
			}
		}
		// Git's own guard is the second net: a fast-forward never
		// overwrites local changes or untracked files.
		if _, err := git(ctx, wt, "merge", "--ff-only", cand.Revision); err != nil {
			return fault.Wrap(err, fault.CodeIntegrationFailed, "fast-forward %s in %s", target, wt)
		}
		return nil
	}
	if _, err := git(ctx, m.Repo, "update-ref", "refs/heads/"+target, cand.Revision, cand.Base); err != nil {
		return fault.Wrap(ErrTargetMoved, fault.CodeIntegrationFailed, "target branch %s moved during verification (%v)", target, err)
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
	common, err := git(ctx, m.Repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "locate git directory")
	}
	path := filepath.Join(strings.TrimSpace(common), "at-promote.lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "open promotion lock")
	}
	delay := 10 * time.Millisecond
	for {
		locked, err := tryLockFile(f)
		if err != nil {
			f.Close()
			return nil, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "take promotion lock")
		}
		if locked {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			return func() { unlockFile(f); f.Close() }, nil
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fault.Wrap(ctx.Err(), fault.CodeIntegrationFailed, "waiting for the promotion lock %s", path)
		case <-time.After(delay + time.Duration(rand.Int63n(int64(delay)))):
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
		return "", fault.Wrap(err, fault.CodeWorkspaceUnavailable, "list worktrees")
	}
	var path string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
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
		if _, err := git(ctx, dir, "-c", "user.name=at", "-c", "user.email=at@localhost", "commit", "-q", "--allow-empty", "-m", "at: initialize repository"); err != nil {
			return false, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "create initial commit in %s", dir)
		}
		return false, nil
	}
	if _, err := git(ctx, dir, "init", "-q", "-b", "main"); err != nil {
		if _, err2 := git(ctx, dir, "init", "-q"); err2 != nil {
			return false, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "git init in %s", dir)
		}
	}
	if _, err := git(ctx, dir, "-c", "user.name=at", "-c", "user.email=at@localhost", "commit", "-q", "--allow-empty", "-m", "at: initialize repository"); err != nil {
		return false, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "create initial commit in %s", dir)
	}
	return true, nil
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}
