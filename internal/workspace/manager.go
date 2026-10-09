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
	// Dirty reports uncommitted changes left by an earlier attempt.
	Dirty bool
}

// TaskBranch names the branch of a task.
func TaskBranch(taskID string) string { return "at/" + taskID }

// EnsureTask creates the task's worktree on branch at/<id> from base (a
// branch name or revision) or reuses it when it already exists.
func (m Manager) EnsureTask(ctx context.Context, taskID string, base string) (Info, error) {
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
		return info, nil
	}
	if m.BranchExists(ctx, info.Branch) {
		// Branch exists (a previous crash between branch creation and
		// worktree registration, or a pruned worktree); attach to it.
		_, _ = git(ctx, m.Repo, "worktree", "prune")
		if _, err := gitRetry(ctx, m.Repo, "worktree", "add", info.Path, info.Branch); err != nil {
			return info, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "attach worktree for %s", info.Branch)
		}
		return info, nil
	}
	if _, err := gitRetry(ctx, m.Repo, "worktree", "add", "-b", info.Branch, info.Path, base); err != nil {
		return info, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "create worktree for %s from %s", info.Branch, base)
	}
	return info, nil
}

// BranchExists reports whether a local branch exists.
func (m Manager) BranchExists(ctx context.Context, branch string) bool {
	_, err := git(ctx, m.Repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
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
			msg := strings.TrimSpace(conflicts)
			if msg != "" {
				msg = " (conflicts: " + strings.ReplaceAll(msg, "\n", ", ") + ")"
			}
			return Candidate{}, fault.Wrap(err, fault.CodeIntegrationFailed, "merging %s into %s failed%s", short(src), target, msg)
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
		if !st.Clean() {
			return fault.New(fault.CodeIntegrationFailed, "target branch %s is checked out in %s with uncommitted changes; commit or stash them before promotion", target, wt)
		}
		if st.Revision != cand.Base {
			return fault.Wrap(ErrTargetMoved, fault.CodeIntegrationFailed, "target branch %s moved from %s to %s during verification", target, short(cand.Base), short(st.Revision))
		}
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

// promoteLock takes a repository-wide lock for promotions into a checked
// out target: a lock file in the common Git directory, created
// exclusively, held for the few milliseconds a fast-forward takes. A lock
// older than promoteLockStale is treated as left behind by a dead process.
func (m Manager) promoteLock(ctx context.Context) (func(), error) {
	common, err := git(ctx, m.Repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "locate git directory")
	}
	path := filepath.Join(strings.TrimSpace(common), "at-promote.lock")
	delay := 10 * time.Millisecond
	for {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "take promotion lock")
		}
		if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) > promoteLockStale {
			os.Remove(path)
			continue
		}
		select {
		case <-ctx.Done():
			return nil, fault.Wrap(ctx.Err(), fault.CodeIntegrationFailed, "waiting for the promotion lock %s", path)
		case <-time.After(delay + time.Duration(rand.Int63n(int64(delay)))):
		}
		if delay < 200*time.Millisecond {
			delay *= 2
		}
	}
}

const promoteLockStale = 5 * time.Minute

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
