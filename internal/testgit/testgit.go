// Package testgit owns how tests run Git: with the user's configuration
// removed, so no test can sign a commit or wait on a passphrase prompt, and
// with Git's upward repository discovery fenced off at the test temp root:
// a stray .git above it (an empty one in HOME, say, when the sandbox puts
// TMPDIR under HOME) must never be adopted by a test's init or commit.
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

const (
	ceilingVar       = "GIT_CEILING_DIRECTORIES"
	acrossFSVar      = "GIT_DISCOVERY_ACROSS_FILESYSTEM"
	homeVar          = "HOME"
	pathListSeparate = string(os.PathListSeparator)
)

// discoveryCeilings lists the directories Git must not climb into: the
// parent of the temp root, plus the real HOME when the temp root is under it.
func discoveryCeilings() string {
	tempRoot := resolved(os.TempDir())
	ceilings := []string{filepath.Dir(tempRoot)}
	if home := resolved(os.Getenv(homeVar)); home != "" && strings.HasPrefix(tempRoot, home+string(filepath.Separator)) {
		ceilings = append(ceilings, home)
	}
	return strings.Join(ceilings, pathListSeparate)
}

func resolved(path string) string {
	if path == "" {
		return ""
	}
	if target, err := filepath.EvalSymlinks(path); err == nil {
		return target
	}
	return filepath.Clean(path)
}

// Overrides returns KEY=VALUE pairs that cut Git (and anything it spawns,
// such as gpg or hooks) off from the user's configuration. home must be a
// directory the caller owns.
func Overrides(home string) ([]string, error) {
	empty := filepath.Join(home, "empty.gitconfig")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		return nil, fmt.Errorf("write empty git config in %s: %w", home, err)
	}
	return []string{
		ceilingVar + "=" + discoveryCeilings(),
		acrossFSVar + "=0",
		homeVar + "=" + home,
		// workspace.CheckTmpRootEnv (testgit cannot import workspace: its
		// tests import testgit): checks' TMPDIRs go under the test's own
		// home, so no test leaves directories in the shared /tmp.
		"ATASK_CHECK_TMP_ROOT=" + filepath.Join(home, "check-tmp"),
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
	return CommandContext(context.Background(), t, dir, args...)
}

// CommandContext is Command bound to ctx.
func CommandContext(ctx context.Context, t testing.TB, dir string, args ...string) *exec.Cmd {
	t.Helper()
	env, err := Overrides(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	full := append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)
	cmd := exec.CommandContext(ctx, "git")
	cmd.Args = append(cmd.Args, full...)
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

// WriteExecutable writes an executable script at path for tests that put a
// fake tool on PATH.
func WriteExecutable(t testing.TB, path, content string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if err := f.Chmod(0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
