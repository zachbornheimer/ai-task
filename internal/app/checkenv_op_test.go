package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/process"
	"github.com/zachbornheimer/ai-task/internal/proof"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

func grantOPToken(t *testing.T) {
	t.Helper()
	cfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "atask"), 0o750); err != nil {
		t.Fatal(err)
	}
	toml := "[sandbox]\nextra_env = [\"" + proof.OPTokenEnv + "\"]\n"
	if err := os.WriteFile(filepath.Join(cfg, "atask", "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", cfg)
}

// The 1Password CLI cannot start inside the sandbox on the token alone: it
// wants a config directory, ~/.config/op is denied, and it fails at once.
// A check handed the token therefore gets a private directory of its own
// that op accepts (op refuses one others can read).
func TestCheckEnvGivesATokenHolderAPrivateOPConfigDir(t *testing.T) {
	grantOPToken(t)
	t.Setenv(proof.OPTokenEnv, "ops_token")
	dir := filepath.Join(t.TempDir(), "worktree")

	env := checkEnv(dir)

	got := envValues(env, opConfigDirEnv)
	if len(got) != 1 {
		t.Fatalf("OP_CONFIG_DIR set %d times, want once: %v", len(got), got)
	}
	tmp := envValues(env, "TMPDIR")[0]
	if filepath.Dir(got[0]) != tmp {
		t.Errorf("OP_CONFIG_DIR %s is not inside the check's TMPDIR %s", got[0], tmp)
	}
	info, err := os.Stat(got[0])
	if err != nil || !info.IsDir() {
		t.Fatalf("OP_CONFIG_DIR %s is not a directory: %v", got[0], err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("OP_CONFIG_DIR is mode %o; op refuses anything but 700", perm)
	}
	t.Cleanup(func() { workspace.RemoveCheckTmp(dir) })
}

// Nothing else about a check's environment changes: without the token (not
// granted, or not set) there is no OP_CONFIG_DIR.
func TestCheckEnvLeavesOPConfigAloneWithoutTheToken(t *testing.T) {
	t.Setenv(proof.OPTokenEnv, "ops_token")
	t.Setenv("OP_CONFIG_DIR", "/should/not/pass")
	dir := filepath.Join(t.TempDir(), "worktree")
	t.Cleanup(func() { workspace.RemoveCheckTmp(dir) })

	if got := envValues(checkEnv(dir), opConfigDirEnv); len(got) != 0 {
		t.Errorf("token not granted, yet OP_CONFIG_DIR reached the check: %v", got)
	}

	grantOPToken(t)
	t.Setenv(proof.OPTokenEnv, "")
	if got := envValues(checkEnv(dir), opConfigDirEnv); len(got) != 0 {
		t.Errorf("token granted but unset, yet OP_CONFIG_DIR reached the check: %v", got)
	}
}

// Under the real Seatbelt profile the directory must be writable: it is the
// one place op can put its config while ~/.config/op stays denied.
func TestSandboxedCheckCanUseItsOPConfigDir(t *testing.T) {
	if runtime.GOOS != "darwin" || process.Inherited() {
		t.Skip("needs a fresh Seatbelt sandbox on macOS")
	}
	grantOPToken(t)
	t.Setenv(proof.OPTokenEnv, "ops_token")
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("no home directory: %v", err)
	}
	dir := filepath.Join(home, ".local", "state", "atask", "workspaces", "proj", "snap")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { workspace.RemoveCheckTmp(dir) })
	env := checkEnv(dir)
	sb := sandboxPolicy(proof.SandboxConfig{Mode: process.SandboxStrict})(dir, env)

	r := process.Run(context.Background(), process.Spec{
		Argv: []string{"sh", "-c", `touch "$OP_CONFIG_DIR/config" && test "$(stat -f %Lp "$OP_CONFIG_DIR")" = 700 && echo ok`},
		Dir:  dir, Env: env, Timeout: time.Minute, Sandbox: sb,
	})
	if r.ExitCode != 0 || strings.TrimSpace(r.Stdout) != "ok" {
		t.Fatalf("a sandboxed check cannot use OP_CONFIG_DIR: %+v", r)
	}
	if !slices.ContainsFunc(sb.Unreadable, func(p string) bool { return strings.HasSuffix(p, ".config/op") }) {
		t.Fatal("the sandbox must keep denying ~/.config/op; this change grants it nothing")
	}
}
