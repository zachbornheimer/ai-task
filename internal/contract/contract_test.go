package contract

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above test directory")
		}
		dir = parent
	}
}

func TestContractDocParses(t *testing.T) {
	s, err := Parse(filepath.Join(repoRoot(t), "docs", "contract.md"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	cases := []struct {
		name    string
		got     []string
		present []string
		absent  []string
	}{
		{"verbs", s.Verbs, []string{"list", "claim renew", "claim release", "verify proof", "verify regression", "verify complete"}, []string{"presets"}},
		{"task flags", s.TaskFlags, []string{"--proof", "--regression", "--size", "--on-complete", "--on-complete-detach"}, []string{"--check"}},
		{"project flags", s.ProjectFlags, []string{"--target-branch", "--attempts"}, nil},
		{"global flags", s.GlobalFlags, []string{"--project", "--db", "--json"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, want := range c.present {
				if !slices.Contains(c.got, want) {
					t.Errorf("%s missing %q: %v", c.name, want, c.got)
				}
			}
			for _, bad := range c.absent {
				if slices.Contains(c.got, bad) {
					t.Errorf("%s must not contain %q: %v", c.name, bad, c.got)
				}
			}
		})
	}

	wantEngine := []string{"Apply", "List", "Show", "Claim", "Renew", "Release", "Log", "Verify"}
	if !slices.Equal(s.EngineMethods, wantEngine) {
		t.Errorf("engine methods = %v, want %v", s.EngineMethods, wantEngine)
	}
}

func TestParseTextRejectsMissingSections(t *testing.T) {
	if _, err := ParseText("# nothing here"); err == nil {
		t.Fatal("expected error for document without a CLI block")
	}
}

func TestContractVerbFlagsAndFilters(t *testing.T) {
	s, err := Parse(filepath.Join(repoRoot(t), "docs", "contract.md"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for verb, want := range map[string][]string{
		"claim":           {"--wait"},
		"claim release":   {"--note", "--failed"},
		"verify complete": {"--reverify"},
		"add":             {"-f", "--proof", "--key"},
		"update":          {"--archive", "--reason", "--reset-attempts", "--remove-requires", "--proof"},
		"init":            {"--name", "--path"},
		"project":         {"--target-branch", "--attempts"},
	} {
		for _, f := range want {
			if !slices.Contains(s.VerbFlags[verb], f) {
				t.Errorf("%s flags lack %q: %v", verb, f, s.VerbFlags[verb])
			}
		}
	}
	if slices.Contains(s.VerbFlags["add"], "--reason") {
		t.Errorf("add must not take update-only flags: %v", s.VerbFlags["add"])
	}
	if !slices.Equal(s.ListFilters, []string{"all", "open", "ready", "blocked", "draft", "complete", "archived"}) {
		t.Errorf("list filters = %v", s.ListFilters)
	}
}
