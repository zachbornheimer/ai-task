package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/proof"
)

// checkEnvNames are the parent variables a check may see by exact name.
var checkEnvNames = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "TERM", "TMPDIR", "CI"}

// checkEnvPrefixes admit every variable that starts with one of them.
var checkEnvPrefixes = []string{
	"LC_", "GO", "GIT_", "MISE_", "NODE_", "NPM_CONFIG_", "PLAYWRIGHT_", "UV_", "ATASK_",
}

// sessionEnvPrefixes name the session tokens, which no check ever receives.
var sessionEnvPrefixes = []string{"ATASK_SESSION=", "TASKS_SESSION="}

// CheckEnvironment decides which parent variables reach a check, a
// regression or an on_complete action: the fixed allowlist plus the names
// the user adds with `[sandbox] extra_env`.
type CheckEnvironment struct {
	extra []string
}

func NewCheckEnvironment(extraNames []string) CheckEnvironment {
	return CheckEnvironment{extra: extraNames}
}

// Allows reports whether the variable called name may pass.
func (c CheckEnvironment) Allows(name string) bool {
	if slices.Contains(checkEnvNames, name) || slices.Contains(c.extra, name) {
		return true
	}
	for _, p := range checkEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Filter keeps the allowed KEY=value entries of environ, never a session token.
func (c CheckEnvironment) Filter(environ []string) []string {
	var out []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if !c.Allows(name) || hasAnyPrefix(kv, sessionEnvPrefixes) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	return slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(s, p) })
}

// opConfigDirEnv is where the 1Password CLI keeps its config.
const opConfigDirEnv = "OP_CONFIG_DIR"

// grantedCheckEnv is the `[sandbox] extra_env` list: the names the owner
// lets through to checks. A bad config is reported where presets load.
func grantedCheckEnv() []string {
	extra, _ := proof.LoadExtraEnv(proof.ConfigPath())
	return extra
}

// opConfigEnv gives a check that env hands the 1Password token a private
// OP_CONFIG_DIR under its TMPDIR. The sandbox denies ~/.config/op, and op
// cannot start without a config directory it may use, so with the token
// alone it fails at once ("cannot create directory ~/.config/op"); the
// token plus this directory is a complete headless login. op refuses a
// directory others can read, hence 0700. A check not handed the token, or
// without a private TMPDIR, gets nothing: its env is unchanged.
func opConfigEnv(env []string, tmp string) []string {
	if tmp == os.TempDir() || !slices.ContainsFunc(envValues(env, proof.OPTokenEnv), func(v string) bool { return v != "" }) {
		return nil
	}
	dir := filepath.Join(tmp, "op-config")
	// #nosec G302 -- a directory needs the execute bit, and op refuses a config directory others can read
	if os.MkdirAll(dir, 0o700) != nil || os.Chmod(dir, 0o700) != nil { //nolint:gosec // a directory needs the execute bit, and op refuses a config directory others can read
		return nil
	}
	return []string{opConfigDirEnv + "=" + dir}
}
