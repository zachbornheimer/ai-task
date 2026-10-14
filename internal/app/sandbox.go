package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/process"
	"github.com/zachbornheimer/ai-task/internal/proof"
)

// Home-relative locations a check may write: tool caches and the state of
// the tools checks run.
var writableUnderHome = []string{
	"Library/Caches", ".cache", ".npm", ".local/share/mise", ".local/state/mise",
	"go/pkg/mod",
}

// Home-relative locations holding credentials: checks may not read them.
var unreadableUnderHome = []string{
	".ssh", ".aws", ".gnupg", ".config/op", ".config/gh", "Library/Keychains",
	".netrc", ".docker", ".local/state/atask",
}

// systemTempRoot is where macOS keeps per-user temp and cache directories.
const systemTempRoot = "/private/var/folders"

var goCaches = sync.OnceValue(func() []string {
	out, err := process.Command(context.Background(), "", "go", "env", "GOCACHE", "GOMODCACHE").Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
})

// sandboxPolicy builds the confinement of a check running in dir with env.
func sandboxPolicy(cfg proof.SandboxConfig) proof.SandboxPolicy {
	home, err := os.UserHomeDir()
	if err != nil {
		return func(string, []string) *process.Sandbox { return &process.Sandbox{Mode: cfg.Mode} }
	}
	return func(dir string, env []string) *process.Sandbox {
		writable := []string{systemTempRoot, os.TempDir()}
		writable = append(writable, underHome(home, writableUnderHome)...)
		writable = append(writable, goCaches()...)
		writable = append(writable, dir, dir+".tmp", gitCommonDir(dir))
		writable = append(writable, envValues(env, "TMPDIR", "ATASK_CHECK_DIR")...)
		for _, extra := range cfg.ExtraWrite {
			writable = append(writable, expandHome(home, extra))
		}
		return &process.Sandbox{
			Mode: cfg.Mode, Home: home,
			Writable: withoutEmpty(writable), Unreadable: underHome(home, unreadableUnderHome),
		}
	}
}

func underHome(home string, rel []string) []string {
	out := make([]string, len(rel))
	for i, r := range rel {
		out[i] = filepath.Join(home, r)
	}
	return out
}

func expandHome(home, path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return path
}

// gitCommonDir is the repository's shared git directory, where a worktree
// commit writes objects and refs; empty when dir is not in a repository.
func gitCommonDir(dir string) string {
	if dir == "" {
		return ""
	}
	cmd := process.Command(context.Background(), dir, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func envValues(env []string, keys ...string) []string {
	var out []string
	for _, kv := range env {
		for _, k := range keys {
			if v, ok := strings.CutPrefix(kv, k+"="); ok {
				out = append(out, v)
			}
		}
	}
	return out
}

func withoutEmpty(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// loadSandboxConfig reads the [sandbox] table of the user's config.
func loadSandboxConfig() (proof.SandboxConfig, error) {
	path := proof.ConfigPath()
	if path == "" {
		return proof.SandboxConfig{Mode: process.SandboxStrict}, nil
	}
	return proof.LoadSandboxConfig(path)
}

// ensureSandbox fails closed before any check starts when confinement is
// required but unavailable.
func (e *Engine) ensureSandbox() error {
	if e.sandbox.Mode == process.SandboxStrict {
		goCaches() // resolve once now, so it never spends a check's budget
	}
	if err := (&process.Sandbox{Mode: e.sandbox.Mode}).Check(); err != nil {
		return fault.Wrap(err, fault.CodeInvalidInput, "run checks")
	}
	return nil
}
