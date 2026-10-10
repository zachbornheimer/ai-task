// Package workspace owns interaction with Git working trees. Milestone 2
// uses it to identify the immutable revision a submission refers to and to
// confirm that a directory still holds that revision when checks run.
// Milestone 3 adds the task-worktree lifecycle behind a small interface.
package workspace

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// GitState describes a working tree at a point in time.
type GitState struct {
	// Revision is the full commit hash of HEAD.
	Revision string
	// Dirty lists porcelain status lines (tracked changes and untracked
	// files). Ignored files never appear. Empty means clean.
	Dirty []string
}

// Clean reports whether the tree exactly matches Revision.
func (s GitState) Clean() bool { return len(s.Dirty) == 0 }

// IsGitRepo reports whether dir is inside a Git working tree, without
// invoking Git: it looks for a .git directory or file up the tree.
// CurrentBranch returns the branch checked out at dir ("" when dir is not
// in a repository or HEAD is detached).
func CurrentBranch(ctx context.Context, dir string) string {
	if !IsGitRepo(dir) {
		return ""
	}
	out, err := git(ctx, dir, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func IsGitRepo(dir string) bool {
	cur, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	for {
		if _, err := os.Lstat(filepath.Join(cur, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return false
		}
		cur = parent
	}
}

// Inspect returns HEAD and the dirty status of dir. It fails with
// WORKSPACE_UNAVAILABLE when Git is missing, dir is not a repository, or
// the repository has no commits.
func Inspect(ctx context.Context, dir string) (GitState, error) {
	rev, err := git(ctx, dir, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return GitState{}, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "cannot resolve HEAD in %s (is it a Git repository with at least one commit?)", dir)
	}
	status, err := git(ctx, dir, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return GitState{}, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "git status failed in %s", dir)
	}
	st := GitState{Revision: strings.TrimSpace(rev)}
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) != "" {
			st.Dirty = append(st.Dirty, line)
		}
	}
	return st, nil
}

// RequireClean inspects dir and fails with WORKSPACE_DIRTY if anything is
// uncommitted. Nothing is ever staged or committed on the caller's behalf:
// an automatic commit could capture secrets or unintended files.
func RequireClean(ctx context.Context, dir string) (GitState, error) {
	st, err := Inspect(ctx, dir)
	if err != nil {
		return st, err
	}
	if !st.Clean() {
		n := len(st.Dirty)
		sample := st.Dirty
		if n > 5 {
			sample = sample[:5]
		}
		return st, fault.New(fault.CodeWorkspaceDirty, "working tree in %s has %d uncommitted change(s), e.g. %s; commit (or stash/remove) them so the submission names an immutable revision", dir, n, strings.Join(sample, ", "))
	}
	return st, nil
}

// gitRetry runs git and retries briefly when it lost a race for one of
// Git's lock files: many `at` processes claiming at once each add a
// worktree to the same repository, and `git worktree add` takes
// repository-wide locks for a few milliseconds.
func gitRetry(ctx context.Context, dir string, args ...string) (string, error) {
	var out string
	var err error
	delay := 20 * time.Millisecond
	for attempt := 0; attempt < 8; attempt++ {
		out, err = git(ctx, dir, args...)
		if err == nil || !isLockContention(err) {
			return out, err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay + time.Duration(rand.Int63n(int64(delay)))):
		}
		delay *= 2
	}
	return out, err
}

func isLockContention(err error) bool {
	msg := err.Error()
	// "failed to read .git/worktrees/<x>/commondir" is `git worktree add`
	// listing worktrees while another process is still creating one.
	for _, s := range []string{".lock", "could not lock", "Unable to create", "File exists", "unable to write", "cannot lock ref", "commondir", "failed to read .git/worktrees"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return out.String(), nil
}

// ChangedFiles lists the paths that dir's HEAD has changed relative to the
// merge base with ref (the task branch's own work so far).
func ChangedFiles(ctx context.Context, dir, ref string) ([]string, error) {
	out, err := git(ctx, dir, "diff", "--name-only", ref+"...HEAD")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(strings.TrimSpace(out), "\n") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// ChangedBetween lists the paths rev changed relative to its merge base
// with base, as seen from the repository at repo.
func ChangedBetween(ctx context.Context, repo, base, rev string) ([]string, error) {
	out, err := git(ctx, repo, "diff", "--name-only", base+"..."+rev)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(strings.TrimSpace(out), "\n") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}
