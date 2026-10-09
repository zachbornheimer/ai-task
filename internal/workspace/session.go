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
// without the agent carrying the secret. A new claim overwrites it, so a
// stale file only ever holds a superseded token.

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

// hookMarker identifies hooks written by at.
const hookMarker = "# installed by at:"

const postCommitHook = "#!/bin/sh\n" + hookMarker + " renews the task lease on every commit in a task worktree\n" +
	"command -v at >/dev/null 2>&1 && at claim renew >/dev/null 2>&1 || true\n"

// InstallHooks writes the post-commit hook into the repository's hooks
// directory when no foreign hook is present. It returns false when an
// existing hook that is not ours was left alone.
func InstallHooks(ctx context.Context, repo string) (bool, error) {
	out, err := git(ctx, repo, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return false, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "locate hooks dir")
	}
	hooks := strings.TrimSpace(out)
	if !filepath.IsAbs(hooks) {
		hooks = filepath.Join(repo, hooks)
	}
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		return false, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "create hooks dir")
	}
	path := filepath.Join(hooks, "post-commit")
	if existing, err := os.ReadFile(path); err == nil && !strings.Contains(string(existing), hookMarker) {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(postCommitHook), 0o755); err != nil {
		return false, fault.Wrap(err, fault.CodeWorkspaceUnavailable, "write post-commit hook")
	}
	return true, nil
}
