package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/must"
)

// branchAdding commits file on a new branch off main and returns the
// requirement for it, leaving main checked out.
func branchAdding(r *repo, task, file string) Requirement {
	r.t.Helper()
	branch := TaskBranch(task)
	r.git(r.dir, "checkout", "-q", "-b", branch, "main")
	rev := strings.TrimSpace(r.commit(r.dir, file, "work of "+task))
	r.git(r.dir, "checkout", "-q", "main")
	return Requirement{Task: task, Revision: rev}
}

// failingCommitHooks points the repository at a hooks directory whose
// commit-msg hook always fails, as a user's commit hooks can.
func failingCommitHooks(r *repo) {
	r.t.Helper()
	hooks := filepath.Join(r.t.TempDir(), "hooks")
	must.OK(r.t, os.MkdirAll(hooks, 0o750))
	must.OK(r.t, os.WriteFile(filepath.Join(hooks, "commit-msg"), []byte("#!/bin/sh\necho 'commit-msg: command not found' >&2\nexit 1\n"), 0o700)) //nolint:gosec // test hook must be executable
	r.git(r.dir, "config", "core.hooksPath", hooks)
}

// TestScratchMergeIgnoresHooks: atask's scratch merges are internal
// plumbing; a user's commit hooks must not decide whether they succeed.
func TestScratchMergeIgnoresHooks(t *testing.T) {
	t.Run("ResolveBase", func(t *testing.T) {
		r := newRepo(t)
		a := branchAdding(r, "at-aaaaaa", "a.txt")
		c := branchAdding(r, "at-cccccc", "c.txt")
		failingCommitHooks(r)
		res, err := r.mgr.ResolveBase(r.ctx, "main", []Requirement{a, c})
		if err != nil {
			t.Fatalf("ResolveBase with failing commit hook: %v", err)
		}
		if len(res.Merged) != 2 {
			t.Fatalf("merged %+v", res.Merged)
		}
		for _, f := range []string{"a.txt", "c.txt"} {
			if _, err := git(r.ctx, r.dir, "cat-file", "-e", res.Revision+":"+f); err != nil {
				t.Fatalf("base %s lacks %s: %v", res.Revision, f, err)
			}
		}
	})
	t.Run("PrepareMerge", func(t *testing.T) {
		r := newRepo(t)
		a := branchAdding(r, "at-aaaaaa", "a.txt")
		c := branchAdding(r, "at-cccccc", "c.txt")
		failingCommitHooks(r)
		cand, err := r.mgr.PrepareMerge(r.ctx, "main", []string{a.Revision, c.Revision}, "chore: merge")
		if err != nil {
			t.Fatalf("PrepareMerge with failing commit hook: %v", err)
		}
		defer cand.Cleanup()
		for _, f := range []string{"a.txt", "c.txt"} {
			if _, err := git(r.ctx, r.dir, "cat-file", "-e", cand.Revision+":"+f); err != nil {
				t.Fatalf("candidate %s lacks %s: %v", cand.Revision, f, err)
			}
		}
	})
}

// unrelatedBranch commits file on a root-less branch sharing no history
// with main, so merging it fails without leaving any conflicted path.
func unrelatedBranch(r *repo, task, file string) Requirement {
	r.t.Helper()
	branch := TaskBranch(task)
	r.git(r.dir, "checkout", "-q", "--orphan", branch)
	r.git(r.dir, "rm", "-q", "-rf", ".")
	rev := strings.TrimSpace(r.commit(r.dir, file, "root of "+task))
	r.git(r.dir, "checkout", "-q", "main")
	return Requirement{Task: task, Revision: rev}
}

// TestMergeFailureWithoutConflictsIsInternal: a merge git refuses for a
// reason other than a conflict is an infrastructure failure carrying
// git's message, never BASE_CONFLICT with an empty file list.
func TestMergeFailureWithoutConflictsIsInternal(t *testing.T) {
	assertInternal := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("merge of unrelated histories succeeded")
		}
		if fault.Is(err, fault.CodeBaseConflict) {
			t.Fatalf("classified as BASE_CONFLICT: %v", err)
		}
		if !fault.IsInternal(err) {
			t.Fatalf("not an internal error: %v", err)
		}
		if !strings.Contains(err.Error(), "unrelated histories") {
			t.Fatalf("error lacks git's message: %v", err)
		}
	}
	t.Run("ResolveBase", func(t *testing.T) {
		r := newRepo(t)
		a := branchAdding(r, "at-aaaaaa", "a.txt")
		u := unrelatedBranch(r, "at-uuuuuu", "u.txt")
		_, err := r.mgr.ResolveBase(r.ctx, "main", []Requirement{a, u})
		assertInternal(t, err)
	})
	t.Run("PrepareMerge", func(t *testing.T) {
		r := newRepo(t)
		a := branchAdding(r, "at-aaaaaa", "a.txt")
		u := unrelatedBranch(r, "at-uuuuuu", "u.txt")
		_, err := r.mgr.PrepareMerge(r.ctx, "main", []string{a.Revision, u.Revision}, "chore: merge")
		assertInternal(t, err)
	})
}
