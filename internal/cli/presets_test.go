package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/proof"
)

type fakeCustomPreset struct{}

func (fakeCustomPreset) Name() string            { return "e2e_console" }
func (fakeCustomPreset) Options() []string       { return []string{"grep"} }
func (fakeCustomPreset) DefaultSize() proof.Size { return proof.SizeMedium }
func (fakeCustomPreset) Subject() string         { return "SPEC" }
func (fakeCustomPreset) Argv(proof.ProofSpec, proof.Env) ([]string, error) {
	return nil, nil
}

func presetsOutput(t *testing.T, args ...string) string {
	t.Helper()
	var out, errb bytes.Buffer
	code := Main(Env{
		Args:   append(args, "presets"),
		Stdout: &out,
		Stderr: &errb,
		Cwd:    t.TempDir(),
		Getenv: func(k string) string {
			if k == "ATASK_DB" {
				return t.TempDir() + "/missing/no.db"
			}
			return ""
		},
	})
	if code != ExitOK {
		t.Fatalf("presets exit %d: %s", code, errb.String())
	}
	return out.String()
}

func TestPresetsCommandLists(t *testing.T) {
	builtins := proof.DefaultRegistry()

	text := presetsOutput(t)
	for _, name := range builtins.List() {
		if !strings.Contains(text, name) {
			t.Fatalf("text output lacks built-in %q:\n%s", name, text)
		}
	}
	for _, col := range []string{"NAME", "SUBJECT", "OPTIONS", "SIZE"} {
		if !strings.Contains(text, col) {
			t.Fatalf("text output lacks column %q:\n%s", col, text)
		}
	}

	type row struct {
		Name    string   `json:"name"`
		Subject string   `json:"subject"`
		Options []string `json:"options"`
		Size    string   `json:"size"`
		Custom  bool     `json:"custom"`
	}
	decode := func(s string) []row {
		var env struct {
			OK     bool `json:"ok"`
			Result struct {
				Presets []row `json:"presets"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(s), &env); err != nil || !env.OK {
			t.Fatalf("bad envelope %q: %v", s, err)
		}
		return env.Result.Presets
	}
	rows := decode(presetsOutput(t, "--json"))
	if len(rows) != len(builtins.List()) {
		t.Fatalf("got %d rows, want %d", len(rows), len(builtins.List()))
	}
	for i, name := range builtins.List() {
		if rows[i].Name != name || rows[i].Custom || rows[i].Options == nil || rows[i].Size == "" {
			t.Fatalf("row %d = %+v, want built-in %q", i, rows[i], name)
		}
	}

	custom := proof.NewRegistry()
	custom.Register(fakeCustomPreset{})
	restore := presetSources
	presetSources = func() (*proof.Registry, *proof.Registry, error) { return builtins, custom, nil }
	defer func() { presetSources = restore }()

	rows = decode(presetsOutput(t, "--json"))
	last := rows[len(rows)-1]
	if last.Name != "e2e_console" || !last.Custom || last.Size != "medium" || last.Subject != "SPEC" || len(last.Options) != 1 {
		t.Fatalf("custom row = %+v", last)
	}
	if rows[0].Custom {
		t.Fatalf("built-ins must come first: %+v", rows)
	}
}
