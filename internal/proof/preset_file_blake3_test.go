package proof

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// blake3 of the empty input (official test vector).
const emptyBlake3 = "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262"

func TestPresetFileBlake3(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "empty"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(in string) RunResult {
		spec, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		return NewRunner().Run(context.Background(), CheckProof, spec, dir)
	}
	cases := []struct {
		name, in string
		pass     bool
		reason   string
	}{
		{"match", "file_blake3: empty hash=" + emptyBlake3, true, ""},
		{"match upper", "file_blake3: empty hash=" + strings.ToUpper(emptyBlake3), true, ""},
		{"mismatch", "file_blake3: empty hash=" + strings.Repeat("0", 64), false, "mismatch"},
		{"missing file", "file_blake3: nope hash=" + emptyBlake3, false, "nope"},
		{"missing hash", "file_blake3: empty", false, "hash"},
		{"short hash", "file_blake3: empty hash=abc", false, "64"},
		{"non-hex hash", "file_blake3: empty hash=" + strings.Repeat("z", 64), false, "hex"},
		{"escape", "file_blake3: ../empty hash=" + emptyBlake3, false, "outside"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := run(c.in)
			if got.Passed != c.pass {
				t.Fatalf("Passed = %v, want %v (output %q)", got.Passed, c.pass, got.Output)
			}
			if !strings.Contains(got.Output, c.reason) {
				t.Errorf("output %q lacks %q", got.Output, c.reason)
			}
		})
	}
}
