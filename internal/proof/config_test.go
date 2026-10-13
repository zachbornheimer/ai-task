package proof

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCustomPresetsFromConfig(t *testing.T) {
	const good = `
[presets.e2e_console]
command = ["npx", "playwright", "test", "{subject}", "--grep", "{grep}"]
options = ["grep"]
size = "medium"
`
	presets, err := LoadConfig(writeConfig(t, good))
	if err != nil || len(presets) != 1 {
		t.Fatalf("LoadConfig = %v, %v", presets, err)
	}
	reg := DefaultRegistry()
	if err := RegisterCustom(reg, presets); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Lookup("run"); !ok {
		t.Fatal("built-in run lost")
	}

	t.Run("substitutes and sizes", func(t *testing.T) {
		spec, err := reg.Parse("e2e_console: spec.ts grep=smoke")
		if err != nil {
			t.Fatal(err)
		}
		p, _ := reg.Lookup("e2e_console")
		argv, err := p.Argv(spec, Env{})
		want := []string{"npx", "playwright", "test", "spec.ts", "--grep", "smoke"}
		if err != nil || !reflect.DeepEqual(argv, want) {
			t.Fatalf("argv = %v, %v; want %v", argv, err, want)
		}
		if p.DefaultSize() != SizeMedium || spec.Size != SizeMedium {
			t.Errorf("size = %v / %v, want medium", p.DefaultSize(), spec.Size)
		}
	})

	t.Run("missing declared option is INVALID_INPUT at parse", func(t *testing.T) {
		_, err := reg.Parse("e2e_console: spec.ts")
		if fault.CodeOf(err) != fault.CodeInvalidInput || !strings.Contains(err.Error(), "grep") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("cannot shadow a built-in", func(t *testing.T) {
		for _, name := range []string{"run", "go_test"} {
			body := "[presets." + name + "]\ncommand = [\"x\"]\n"
			if _, err := LoadConfig(writeConfig(t, body)); fault.CodeOf(err) != fault.CodeInvalidInput {
				t.Errorf("%s: err = %v", name, err)
			}
		}
		if err := RegisterCustom(reg, []Preset{runLike("run")}); err == nil {
			t.Error("RegisterCustom allowed shadowing run")
		}
	})

	t.Run("malformed toml names the file", func(t *testing.T) {
		path := writeConfig(t, "[presets.x\n")
		_, err := LoadConfig(path)
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("missing file is empty", func(t *testing.T) {
		presets, err := LoadConfig(filepath.Join(t.TempDir(), "absent.toml"))
		if err != nil || len(presets) != 0 {
			t.Fatalf("= %v, %v", presets, err)
		}
	})

	t.Run("config path honors XDG_CONFIG_HOME", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/x/cfg")
		if got := ConfigPath(); got != "/x/cfg/atask/config.toml" {
			t.Errorf("ConfigPath = %q", got)
		}
	})
}

func runLike(name string) Preset { return CustomPreset{name: name, command: []string{"x"}} }
