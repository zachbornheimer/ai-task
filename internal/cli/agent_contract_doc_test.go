package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnknownAgentVerbsAreRejected(t *testing.T) {
	for _, v := range unknownAgentVerbs {
		t.Run(v, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := Main(Env{
				Args:   []string{v},
				Stdin:  strings.NewReader(""),
				Stdout: &out,
				Stderr: &errb,
				Cwd:    t.TempDir(),
				Getenv: func(string) string { return "" },
			})
			if code == ExitOK {
				t.Fatalf("%s must not be a command", v)
			}
			want := `unknown command "` + v + `"`
			if !strings.Contains(errb.String(), want) {
				t.Fatalf("%s: want %s, got %s", v, want, errb.String())
			}
		})
	}
}

func TestAgentContractDocMatchesSurface(t *testing.T) {
	doc := readAgentContractDoc(t)
	lower := strings.ToLower(doc)

	for _, v := range everydayAgentVerbs {
		if !strings.Contains(doc, "`"+v+"`") && !strings.Contains(doc, "atask "+v) {
			t.Errorf("docs/agent-contract.md does not name everyday verb %q", v)
		}
	}
	if !strings.Contains(lower, "unknown command") {
		t.Errorf("docs/agent-contract.md must call whoami, ready, and comment unknown commands")
	}
	for _, v := range unknownAgentVerbs {
		if !unknownVerbDocumented(doc, v) {
			t.Errorf("docs/agent-contract.md does not document %q as an unknown command", v)
		}
	}
	if !strings.Contains(lower, "current claim") {
		t.Errorf("docs/agent-contract.md must say show with no argument is the current claim")
	}
	if !strings.Contains(lower, "list draft") && !strings.Contains(lower, "draft") {
		t.Errorf("docs/agent-contract.md must document draft tasks")
	}
}

func unknownVerbDocumented(doc, verb string) bool {
	head := doc
	if i := strings.Index(doc, "\n## "); i > 0 {
		head = doc[:i]
	}
	compact := strings.ToLower(strings.Join(strings.Fields(head), " "))
	return strings.Contains(compact, verb) && strings.Contains(compact, "unknown command")
}

func readAgentContractDoc(t *testing.T) string {
	t.Helper()
	p := filepath.Join("..", "..", "docs", "agent-contract.md")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}
