package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// removedSurface matches every verb and flag docs/contract.md does not list.
var removedSurface = regexp.MustCompile(`--cohort|--check\b|--optional-check|--planner|atask hook|--claude\b|--no-instructions|--pin\b|--lease\b|--session\b|--output(\s|=|$)|--pretty|--policy-json|--expect-rev|--idempotency-key|--set-requires|verify groups|integration promote|awaiting_integration`)

// Docs name only contract surface. docs/migration.md maps old names to new
// ones, docs/decisions and docs/plans are history, and docs/contract.md is
// the surface itself.
func TestDocsNameOnlyContractSurface(t *testing.T) {
	root := filepath.Join("..", "..")
	docs := []string{filepath.Join(root, "README.md"), filepath.Join(root, "AGENTS.md")}
	for _, pattern := range []string{"docs/*.md", "examples/hosts/*"} {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, matches...)
	}
	for _, path := range docs {
		switch filepath.Base(path) {
		case "migration.md", "contract.md":
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if m := removedSurface.FindString(line); m != "" {
				t.Errorf("%s:%d names %q, which is outside docs/contract.md:\n  %s", path, i+1, m, strings.TrimSpace(line))
			}
		}
	}
}
