package app

import (
	"slices"
	"strings"
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
