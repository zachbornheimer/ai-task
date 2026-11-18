package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseSwitchPathPrettyJSONWithPreamble(t *testing.T) {
	// portable-home-ok: fixture mirrors wt's absolute path field, not this machine.
	out := []byte("\u25ce Creating worktree for atask/at-v0kbh6...\nPreparing worktree (new branch 'atask/at-v0kbh6')\nHEAD is now at 91bd15d Merge pull request #129...\n\u2713 Created branch atask/at-v0kbh6\n{\n  \"action\": \"created\",\n  \"branch\": \"atask/at-v0kbh6\",\n  \"path\": \"/worktrees/dev-env-atask-at-v0kbh6\",\n  \"created_branch\": true,\n  \"base_branch\": \"91bd15dff86aad6dafe6e75b137850537aa76297\"\n}\n")
	path, err := parseSwitchPath(out)
	if err != nil || path != "/worktrees/dev-env-atask-at-v0kbh6" {
		t.Fatalf("%q %v", path, err)
	}
}

func TestParseSwitchPathSkipsBlankAndBrokenObjects(t *testing.T) {
	for _, out := range []string{
		"progress {not json} result: {\"path\":\"/tmp/ws\"} finished",
		"progress {\"path\":\"\"} result: {\"path\":\"/tmp/ws\"} finished",
	} {
		path, err := parseSwitchPath([]byte(out))
		if err != nil || path != "/tmp/ws" {
			t.Fatalf("output %q: %q %v", out, path, err)
		}
	}
	for _, out := range []string{"{\"path\":\"\"}", "{\"path\":\"  \"}"} {
		if _, err := parseSwitchPath([]byte(out)); err == nil || !strings.Contains(err.Error(), "wt switch --format json did not include path") {
			t.Fatalf("output %q: %v", out, err)
		}
	}
}

func TestParseSwitchPathJSONLine(t *testing.T) {
	out := []byte("{\"action\":\"created\",\"branch\":\"atask/x\",\"path\":\"/tmp/ws\"}\n○ Switched\n")
	path, err := parseSwitchPath(out)
	if err != nil || path != "/tmp/ws" {
		t.Fatalf("%q %v", path, err)
	}
}

func TestParseSwitchPathMissing(t *testing.T) {
	if _, err := parseSwitchPath([]byte("not json\n")); err == nil {
		t.Fatal("expected error")
	}
}

func TestWorktrunkSwitcherRequiresWt(t *testing.T) {
	s := WorktrunkSwitcher{LookPath: func(string) (string, error) { return "", errors.New("not found") }}
	_, err := s.Ensure(context.Background(), t.TempDir(), "atask/x", "main", true)
	if err == nil || !strings.Contains(err.Error(), "Worktrunk") {
		t.Fatalf("%v", err)
	}
}

func TestWorktrunkSwitcherArgs(t *testing.T) {
	var got []string
	s := WorktrunkSwitcher{
		LookPath: func(string) (string, error) { return "/bin/wt", nil },
		Run: func(_ context.Context, wt, dir string, args []string) ([]byte, error) {
			got = append([]string{wt, dir}, args...)
			return []byte("{\"action\":\"created\",\"branch\":\"atask/x\",\"path\":\"/tmp/out\"}\n"), nil
		},
	}
	path, err := s.Ensure(context.Background(), "/repo", "atask/x", "main", true)
	if err != nil || path != "/tmp/out" {
		t.Fatalf("%q %v", path, err)
	}
	want := []string{"/bin/wt", "/repo", "switch", "--no-cd", "--no-hooks", "--yes", "--format", "json", "--create", "--base", "main", "atask/x"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("got %v want %v", got, want)
	}
}
