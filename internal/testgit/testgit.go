// Package testgit owns how tests run Git: with the user's configuration
// removed, so no test can sign a commit or wait on a passphrase prompt.
package testgit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Overrides returns KEY=VALUE pairs that cut Git (and anything it spawns,
// such as gpg or hooks) off from the user's configuration. home must be a
// directory the caller owns.
func Overrides(home string) ([]string, error) {
	empty := filepath.Join(home, "empty.gitconfig")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		return nil, fmt.Errorf("write empty git config in %s: %w", home, err)
	}
	return []string{
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=" + empty,
		"GIT_CONFIG_SYSTEM=" + empty,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GPG_TTY=",
		"HK=0",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	}, nil
}

// Isolate applies Overrides to the test process, so Git run in-process by
// production code under test is isolated too.
func Isolate(t testing.TB) {
	t.Helper()
	env, err := Overrides(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
}

// IsolateMain applies Overrides for a whole package: call from TestMain.
func IsolateMain(m *testing.M) int {
	home, err := os.MkdirTemp("", "testgit-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testgit:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(home) }() // temp dir; the OS reaps leftovers
	env, err := Overrides(home)
	if err != nil {
		fmt.Fprintln(os.Stderr, "testgit:", err)
		return 1
	}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, "testgit:", err)
			return 1
		}
	}
	return m.Run()
}

// Command builds a git command in dir with isolation and no hooks, signing,
// or ambient identity. Callers may append to cmd.Env.
func Command(t testing.TB, dir string, args ...string) *exec.Cmd {
	t.Helper()
	return CommandContext(t, context.Background(), dir, args...)
}

// CommandContext is Command bound to ctx.
func CommandContext(t testing.TB, ctx context.Context, dir string, args ...string) *exec.Cmd {
	t.Helper()
	env, err := Overrides(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	full := append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), env...)
	return cmd
}

// Run executes git in dir and returns trimmed combined output.
func Run(t testing.TB, dir string, args ...string) string {
	t.Helper()
	out, err := Command(t, dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}
