package proof

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPresetFileExistsAbsent(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a.txt", "sub/b.log", "sub/deep/c.log"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name, in string
		pass     bool
		invalid  bool
	}{
		{"exists pass", "file_exists: sub/b.log", true, false},
		{"exists fail", "file_exists: nope.txt", false, false},
		{"absent pass", "file_absent: **/*.tmp", true, false},
		{"absent fail", "file_absent: **/*.log", false, false},
		{"absent depth1 pass", "file_absent: **/c.log depth=1", true, false},
		{"absent depth2 fail", "file_absent: **/b.log depth=2", false, false},
		{"exists escape", "file_exists: ../x", false, true},
		{"exists absolute", "file_exists: /etc/passwd", false, true},
		{"absent escape", "file_absent: ../*.log", false, true},
	}
	r := NewRunner()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec, err := Parse(c.in)
			if err != nil {
				t.Fatal(err)
			}
			res := r.Run(context.Background(), CheckProof, spec, dir)
			if res.Passed != c.pass {
				t.Fatalf("passed=%v want %v: %s", res.Passed, c.pass, res.Output)
			}
			if c.invalid {
				if res.ReturnCode != nil || res.Output == "" {
					t.Fatalf("want rejection with no return code, got %+v", res)
				}
				return
			}
			if res.Output == "" || res.StartedAt.IsZero() || res.Preset != spec.Preset {
				t.Fatalf("bad RunResult shape: %+v", res)
			}
		})
	}
}
