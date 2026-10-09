// Package workspace owns interaction with Git working trees. Milestone 2
// uses it to identify the immutable revision a submission refers to and to
// confirm that a directory still holds that revision when checks run.
// Milestone 3 adds the task-worktree lifecycle behind a small interface.
package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
