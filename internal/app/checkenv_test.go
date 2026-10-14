package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setSecrets(t *testing.T) {
	t.Helper()
	for _, k := range []string{"AWS_SECRET_ACCESS_KEY", "OP_SERVICE_ACCOUNT_TOKEN", "GITHUB_TOKEN", "ANTHROPIC_API_KEY"} {
		t.Setenv(k, "leak-"+k)
	}
	t.Setenv("GOFLAGS", "-mod=mod")
}

// envProbe fails when a secret is visible or an expected variable is missing.
const envProbe = `env | grep -E 'AWS_SECRET|OP_SERVICE|GITHUB_TOKEN|ANTHROPIC_API' && exit 1
test -n "$PATH" && test -n "$HOME" && test "$GOFLAGS" = "-mod=mod -count=1" && test -n "$ATASK_CHECK_DIR"`

func TestCheckEnvIsAllowlisted(t *testing.T) {
	setSecrets(t)
	f := newGitFixture(t)
	a := f.addWith("a", envProbe)
	f.complete(f.claim(string(a)))
}

func TestCheckEnvPassesExtraEnvNames(t *testing.T) {
	setSecrets(t)
	cfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "atask"), 0o750); err != nil {
		t.Fatal(err)
	}
	toml := "[sandbox]\nextra_env = [\"GITHUB_TOKEN\"]\n"
	if err := os.WriteFile(filepath.Join(cfg, "atask", "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", cfg)
	f := newGitFixture(t)
	probe := strings.ReplaceAll(envProbe, "|GITHUB_TOKEN", "")
	a := f.addWith("a", `test "$GITHUB_TOKEN" = leak-GITHUB_TOKEN && `+probe)
	f.complete(f.claim(string(a)))
}
