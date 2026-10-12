package app_test

import (
	"os"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
)

// TestAgentsMDIsTheInstalledBlock keeps this repository's AGENTS.md
// identical to what `atask init` installs (between the markers) and the
// rules an agent gets from the installed AGENTS.md block.
func TestAgentsMDIsTheInstalledBlock(t *testing.T) {
	b, err := os.ReadFile("../../AGENTS.md")
	if err != nil {
		t.Skip("AGENTS.md not available")
	}
	s := string(b)
	block := app.InstructionsBegin + "\n" + app.Instructions() + app.InstructionsEnd
	if !strings.Contains(s, block) {
		t.Fatalf("AGENTS.md does not hold the installed block verbatim; run `atask init --path .` to refresh it")
	}
	if !strings.Contains(app.Instructions(), app.Rules) {
		t.Fatal("the instruction block must contain the execution contract")
	}
}
