package proof

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/testgit"
)

// fakeMiseOnPath installs a `mise` that echoes its argv, so the spawned
// command line is observable without a real toolchain.
func fakeMiseOnPath(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"MISE-ARGV: $*\"\n"
	testgit.WriteExecutable(t, filepath.Join(bin, "mise"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeMiseConfig(t *testing.T, dir, rel string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[tools]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPresetsRunUnderMise(t *testing.T) {
	fakeMiseOnPath(t)
	spec, err := Parse("run: go version")
	if err != nil {
		t.Fatal(err)
	}
	run := func(dir string, opts ...RunnerOption) string {
		return NewRunner(opts...).Run(context.Background(), CheckProof, spec, dir).Output
	}

	t.Run("wrapped when any config file exists", func(t *testing.T) {
		for _, rel := range []string{"mise.toml", ".mise.toml", ".config/mise/config.toml", "mise.local.toml"} {
			dir := t.TempDir()
			writeMiseConfig(t, dir, rel)
			if !HasMiseConfig(dir) {
				t.Errorf("HasMiseConfig missed %s", rel)
			}
			if out := run(dir); !strings.Contains(out, "MISE-ARGV: exec -- go version") {
				t.Errorf("%s: output %q lacks mise exec -- go version", rel, out)
			}
		}
	})
	t.Run("unwrapped without config", func(t *testing.T) {
		dir := t.TempDir()
		if HasMiseConfig(dir) {
			t.Error("HasMiseConfig true for empty dir")
		}
		if out := run(dir); strings.Contains(out, "MISE-ARGV") || !strings.Contains(out, "go version") {
			t.Errorf("unwrapped output = %q", out)
		}
	})
	t.Run("detector is injectable", func(t *testing.T) {
		out := run(t.TempDir(), WithMiseDetector(func(string) bool { return true }))
		if !strings.Contains(out, "MISE-ARGV: exec -- go version") {
			t.Errorf("injected detector ignored: %q", out)
		}
	})
}
