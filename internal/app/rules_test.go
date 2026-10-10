package app_test

import (
	"os"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
)

// TestRulesMatchAgentsMD keeps the contract an agent gets from
// `at context --with-rules` identical to the one in AGENTS.md.
func TestRulesMatchAgentsMD(t *testing.T) {
	b, err := os.ReadFile("../../AGENTS.md")
	if err != nil {
		t.Skip("AGENTS.md not available")
	}
	if !strings.Contains(string(b), strings.TrimSpace(app.Rules)) {
		t.Fatalf("AGENTS.md does not contain the Rules text verbatim; update one of them:\n%s", app.Rules)
	}
}
