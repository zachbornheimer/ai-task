package proof

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

func TestPresetGrep(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "one\ntwo needle\n")
	write("sub/deep/b.txt", "nothing\nhaystack\n")
	write(".git/config", "secret\n")

	cases := []struct {
		name    string
		path    string
		opts    map[string]string
		passed  bool
		errCode fault.Code
		reason  string
	}{
		{"match in file", "a.txt", map[string]string{"pattern": "nee+dle"}, true, "", "a.txt:2"},
		{"no match", "a.txt", map[string]string{"pattern": "absent-word"}, false, "", ""},
		{"absent and no match passes", "a.txt", map[string]string{"pattern": "zzz", "absent": "true"}, true, "", ""},
		{"absent and match fails", "a.txt", map[string]string{"pattern": "needle", "absent": "true"}, false, "", "a.txt:2"},
		{"directory recursion", ".", map[string]string{"pattern": "^haystack$"}, true, "", "sub/deep/b.txt:2"},
		{"skips .git", ".", map[string]string{"pattern": "secret"}, false, "", ""},
		{"invalid regex", "a.txt", map[string]string{"pattern": "("}, false, fault.CodeInvalidInput, ""},
		{"missing pattern", "a.txt", map[string]string{}, false, fault.CodeInvalidInput, ""},
		{"escape rejected", "../outside", map[string]string{"pattern": "x"}, false, fault.CodeInvalidInput, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := ProofSpec{Preset: "grep", Subject: c.path, Options: c.opts}
			passed, reason, err := grepPreset{}.Evaluate(spec, dir)
			if c.errCode != "" {
				if fault.CodeOf(err) != c.errCode {
					t.Fatalf("want %s error, got %v", c.errCode, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if passed != c.passed || !strings.Contains(reason, c.reason) {
				t.Fatalf("passed=%v reason=%q, want passed=%v reason containing %q", passed, reason, c.passed, c.reason)
			}
		})
	}

	spec, err := Parse("grep: a.txt pattern=needle absent=true")
	if err != nil {
		t.Fatal(err)
	}
	if res := NewRunner().Run(t.Context(), CheckProof, spec, dir); res.Passed {
		t.Fatalf("runner should fail absent=true on a match: %+v", res)
	}
}
