package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
)

// TestInstallInstructionsIsIdempotent: installing twice leaves AGENTS.md
// byte-for-byte unchanged (one blank line survives after the end marker,
// the second run reports no update and does not write), and a file with a
// stray end marker but no begin marker ends up with exactly one block.
func TestInstallInstructionsIsIdempotent(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// A fresh install, then a second run.
	root := t.TempDir()
	first, err := app.InstallInstructions(root)
	if err != nil || !first.Created || !first.Updated {
		t.Fatalf("first run: %+v %v", first, err)
	}
	before := read(first.AgentsMD)
	if !strings.Contains(before, app.InstructionsEnd+"\n\n## Engineering guidelines") {
		t.Fatalf("expected one blank line after the end marker:\n%s", before)
	}
	// A read-only file makes a stray write fail for ordinary users; the
	// byte comparison below catches it for everyone.
	if err := os.Chmod(first.AgentsMD, 0o400); err != nil {
		t.Fatal(err)
	}
	second, err := app.InstallInstructions(root)
	if err != nil || second.Updated || second.Created {
		t.Fatalf("second run: %+v %v", second, err)
	}
	if after := read(first.AgentsMD); after != before {
		t.Fatalf("second run changed AGENTS.md:\n--- before\n%s\n--- after\n%s", before, after)
	}
	if err := os.Chmod(first.AgentsMD, 0o600); err != nil {
		t.Fatal(err)
	}

	// An out-of-date block is replaced, the surrounding text kept, and the
	// result is then stable.
	stale := "intro\n\n" + app.InstructionsBegin() + "\nold\n" + app.InstructionsEnd + "\n\n# Mine\n"
	if err := os.WriteFile(first.AgentsMD, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := app.InstallInstructions(root); err != nil || !r.Updated {
		t.Fatalf("stale block: %+v %v", r, err)
	}
	updated := read(first.AgentsMD)
	if !strings.HasPrefix(updated, "intro\n\n"+app.InstructionsBegin()) || !strings.HasSuffix(updated, app.InstructionsEnd+"\n\n# Mine\n") {
		t.Fatalf("replacement lost the surrounding text or blank line:\n%s", updated)
	}
	if r, err := app.InstallInstructions(root); err != nil || r.Updated || read(first.AgentsMD) != updated {
		t.Fatalf("not stable after replacement: %+v %v", r, err)
	}

	// An end marker with no begin marker: one block, not two, and stable.
	orphan := t.TempDir()
	path := filepath.Join(orphan, "AGENTS.md")
	if err := os.WriteFile(path, []byte("# Notes\n\n"+app.InstructionsEnd+"\n\n# Mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := app.InstallInstructions(orphan); err != nil || !r.Updated {
		t.Fatalf("orphan end marker: %+v %v", r, err)
	}
	got := read(path)
	if n := strings.Count(got, app.InstructionsBegin()); n != 1 {
		t.Fatalf("begin markers = %d, want 1:\n%s", n, got)
	}
	if n := strings.Count(got, app.InstructionsEnd); n != 1 {
		t.Fatalf("end markers = %d, want 1:\n%s", n, got)
	}
	if !strings.Contains(got, "# Notes") || !strings.Contains(got, "# Mine") {
		t.Fatalf("repository text lost:\n%s", got)
	}
	if r, err := app.InstallInstructions(orphan); err != nil || r.Updated || read(path) != got {
		t.Fatalf("not stable after orphan repair: %+v %v", r, err)
	}
}
