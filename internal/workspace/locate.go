package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalRoot normalises a repository directory for recording and lookup:
// absolute, symlinks resolved, trailing separators removed. Two paths naming
// the same directory canonicalise identically.
func CanonicalRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

// LocateRoot finds the repository a working directory belongs to.
//
// If dir is inside a Git repository, the result is the repository's main
// worktree root, so every linked worktree (including those Worktrunk creates
// elsewhere on disk) resolves to the same repository. Otherwise the result is
// "".
//
// Git is not invoked: a ".git" directory marks the main worktree; a ".git"
// file ("gitdir: <path>") marks a linked worktree whose gitdir lives under
// "<common>/.git/worktrees/<name>".
func LocateRoot(dir string) (string, error) {
	cur, err := CanonicalRoot(dir)
	if err != nil {
		return "", err
	}
	for {
		dotGit := filepath.Join(cur, ".git")
		fi, err := os.Lstat(dotGit)
		if err == nil {
			if fi.IsDir() {
				return cur, nil
			}
			if fi.Mode().IsRegular() {
				root, err := rootFromGitFile(dotGit)
				if err == nil {
					return root, nil
				}
				// Unreadable or unexpected .git file: treat as the root itself.
				return cur, nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", nil
		}
		cur = parent
	}
}

func rootFromGitFile(path string) (string, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(b))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return "", errors.New("unexpected .git file content")
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(filepath.Dir(path), gitdir)
	}
	gitdir = filepath.Clean(gitdir)
	// Linked worktree: <common>/.git/worktrees/<name>
	wt := filepath.Dir(gitdir)
	if filepath.Base(wt) != "worktrees" {
		return "", fmt.Errorf("not a linked worktree gitdir: %s", gitdir)
	}
	common := filepath.Dir(wt) // <common>/.git
	return CanonicalRoot(filepath.Dir(common))
}

// MatchRoot picks the root that contains dir, preferring the longest match.
// It returns "" when no root contains dir.
func MatchRoot(dir string, roots []string) string {
	best := ""
	for _, r := range roots {
		if r == "" {
			continue
		}
		if dir == r || strings.HasPrefix(dir, r+string(filepath.Separator)) {
			if len(r) > len(best) {
				best = r
			}
		}
	}
	return best
}
