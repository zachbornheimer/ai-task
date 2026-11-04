package proof

import (
	"context"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// TestParseRejectsSubjectsOutsideTheWorktree: the path-check presets read
// inside the claim worktree, so an absolute or escaping subject can never
// pass at verify. Parse rejects it at add with the run: escape named,
// instead of accepting a proof that fails after the work is done.
func TestParseRejectsSubjectsOutsideTheWorktree(t *testing.T) {
	cases := []struct{ name, in, wantErr string }{
		{"file_exists absolute", "file_exists: /etc/passwd", "run:"},
		{"file_exists escape", "file_exists: ../x", "run:"},
		{"file_exists empty subject", "file_exists:", "file_exists"},
		{"file_absent absolute", "file_absent: /tmp/scratch", "run:"},
		{"file_absent escape", "file_absent: ../*.log", "run:"},
		{"file_blake3 absolute", "file_blake3: /etc/passwd hash=" + strings.Repeat("0", 64), "run:"},
		{"file_blake3 escape", "file_blake3: ../empty hash=" + strings.Repeat("0", 64), "run:"},
		{"grep absolute", "grep: /etc/passwd pattern=root", "run:"},
		{"grep escape", "grep: ../outside pattern=x", "run:"},
		{"grep empty subject searches the worktree", "grep: pattern=needle", ""},
		{"file_exists relative", "file_exists: a.txt", ""},
		{"grep relative", "grep: a.txt pattern=needle", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse(c.in)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("Parse(%q): %v", c.in, err)
				}
				return
			}
			if !fault.Is(err, fault.CodeInvalidInput) {
				t.Fatalf("Parse(%q) err = %v, want INVALID_INPUT", c.in, err)
			}
			if !strings.Contains(fault.MessageOf(err), c.wantErr) {
				t.Errorf("Parse(%q) error %q lacks %q", c.in, fault.MessageOf(err), c.wantErr)
			}
		})
	}

	// Verify keeps its own guard: a spec that bypassed Parse, such as one
	// built by an embedded runner, is still rejected at run time, with the
	// result shape of a rejected check.
	res := NewRunner().Run(context.Background(), CheckProof, ProofSpec{Preset: "file_exists", Subject: "/etc/passwd"}, t.TempDir())
	if res.Passed || res.ReturnCode != nil || res.Output == "" {
		t.Errorf("Run result = %+v, want rejected check with no return code", res)
	}
}
