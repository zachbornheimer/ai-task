package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/testgit"
)

var contractVerbs = []string{"add", "claim", "context", "list", "log", "show", "update", "verify"}

// TestHostHooksNeverCallHookVerb: init writes one managed AGENTS.md block
// naming only contract verbs and the three managed Claude Code hooks; nothing it
// writes or ships references the removed hook surface.
func TestHostHooksNeverCallHookVerb(t *testing.T) {
	root := t.TempDir()
	testgit.Run(t, root, "init", "-q")
	if _, err := app.InstallInstructions(root); err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(root, "AGENTS.md")
	before := mustRead(t, agents)

	block := regexp.MustCompile(`(?s)<!-- BEGIN atask v:1 hash:[0-9a-f]+ -->\n(.*)<!-- END atask -->`).FindStringSubmatch(before)
	if block == nil {
		t.Fatalf("no managed block:\n%s", before)
	}
	if !strings.Contains(block[1], "Do not use markdown TODO lists for task tracking.") {
		t.Fatalf("block lacks the no-TODO-lists sentence:\n%s", block[1])
	}
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile("`atask ([a-z]+)").FindAllStringSubmatch(block[1], -1) {
		seen[m[1]] = true
	}
	var got []string
	for v := range seen {
		got = append(got, v)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(contractVerbs, ",") {
		t.Fatalf("block verbs = %v, want %v", got, contractVerbs)
	}

	// Same template: no-op. Changed template: only the block is rewritten.
	if r, err := app.InstallInstructions(root); err != nil || r.Updated {
		t.Fatalf("second run: %+v %v", r, err)
	}
	if mustRead(t, agents) != before {
		t.Fatal("second run changed AGENTS.md")
	}
	mine := before + "\n# Mine\n\nkeep me\n"
	mustWrite(t, agents, mine)
	if r, err := app.InstallInstructionsText(root, "\nchanged template `atask context`\n"); err != nil || !r.Updated {
		t.Fatalf("changed template: %+v %v", r, err)
	}
	after := mustRead(t, agents)
	if !strings.Contains(after, "changed template") || !strings.HasSuffix(after, "# Mine\n\nkeep me\n") || strings.Count(after, "BEGIN atask") != 1 {
		t.Fatalf("block not rewritten in place:\n%s", after)
	}

	// The settings: the three managed hooks, and a legacy single hook is replaced.
	mustWrite(t, filepath.Join(root, ".claude", "settings.json"), `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"atask context && atask context --hook"}]}]}}`)
	if _, err := app.InstallInstructions(root); err != nil {
		t.Fatal(err)
	}
	settingsRaw := mustRead(t, filepath.Join(root, ".claude", "settings.json"))
	var settings struct {
		Hooks map[string][]struct {
			Matcher string                           `json:"matcher"`
			Hooks   []struct{ Type, Command string } `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(settingsRaw), &settings); err != nil {
		t.Fatal(err)
	}
	var got3 []string
	for _, ev := range []string{"SessionStart", "UserPromptSubmit"} {
		for _, e := range settings.Hooks[ev] {
			if len(e.Hooks) != 1 {
				t.Fatalf("one command per entry:\n%s", settingsRaw)
			}
			got3 = append(got3, ev+"|"+e.Matcher+"|"+e.Hooks[0].Command)
		}
	}
	want3 := "SessionStart|startup|atask context,SessionStart|compact|resume|atask context --hook,UserPromptSubmit||atask context --hook --changed"
	if len(settings.Hooks) != 2 || strings.Join(got3, ",") != want3 {
		t.Fatalf("settings hooks = %v, want %s\n%s", got3, want3, settingsRaw)
	}

	// The removed hook surface appears nowhere init writes or ships.
	forbidden := regexp.MustCompile(`atask hook|guard-edit|Stop|PreCompact`)
	files := []string{agents, filepath.Join(root, ".claude", "settings.json")}
	for _, pat := range []string{"init*.go", "../app/instructions*.go", "../app/claude_hook*.go", "../../examples/hosts/*"} {
		m, _ := filepath.Glob(pat)
		files = append(files, m...)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		if m := forbidden.FindString(mustRead(t, f)); m != "" {
			t.Errorf("%s contains %q", f, m)
		}
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(p))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}
