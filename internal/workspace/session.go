package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// The session token of a claim is stored in the task worktree's private
// Git directory (`<repo>/.git/worktrees/<name>/at-session`), which is never
// part of the tree. Any `at` command run inside that worktree finds it
// without the agent carrying the secret.
//
// A worktree whose attempt did not end cleanly is quarantined (renamed,
// detached) before the next attempt gets a fresh worktree at the task
// path, so a stale process keeps only its own, expired, token: the new
// token lives in a Git directory the stale process never sees.

const tokenFile = "at-session"

// gitDir resolves the private git directory of the worktree containing
// dir, honouring GIT_DIR (set by Git when running hooks).
func gitDir(ctx context.Context, dir string) (string, error) {
	if g := os.Getenv("GIT_DIR"); g != "" {
		if !filepath.IsAbs(g) {
			g = filepath.Join(dir, g)
		}
		return filepath.Clean(g), nil
	}
	out, err := git(ctx, dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// StoreToken writes the session token for the worktree at dir.
func StoreToken(ctx context.Context, dir, token string) error {
	g, err := gitDir(ctx, dir)
	if err != nil {
		return fault.Wrap(err, fault.CodeWorkspaceUnavailable, "locate git dir of %s", dir)
	}
	return os.WriteFile(filepath.Join(g, tokenFile), []byte(token+"\n"), 0o600)
}

// LoadToken returns the token stored for the worktree containing dir, or
// "" when dir is not inside a task worktree with a stored token.
func LoadToken(ctx context.Context, dir string) string {
	if !IsGitRepo(dir) {
		return ""
	}
	g, err := gitDir(ctx, dir)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(g, tokenFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
