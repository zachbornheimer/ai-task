package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/testgit"
)

const (
	noClaimHookOutput  = "atask_context_version: 1\ntask: null\nstatus: no_active_claim\n"
	hookSessionPayload = `{"session_id":"sess-abc"}`
)

// hookProject is a temp git project with one claimable task.
type hookProject struct {
	t    *testing.T
	repo string
	db   string
	id   string
	ws   string
	tok  string
}

func (h *hookProject) run(cwd, stdin string, args ...string) (int, string) {
	h.t.Helper()
	var out, errb bytes.Buffer
	getenv := func(k string) string {
		if k == "ATASK_DB" {
			return h.db
		}
		return ""
	}
	code := Main(Env{Args: args, Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb, Cwd: cwd, Getenv: getenv})
	return code, out.String() + errb.String()
}

func newHookProject(t *testing.T) *hookProject {
	t.Helper()
	repo := t.TempDir()
	testgit.Run(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	testgit.Run(t, repo, "add", "a.txt")
	testgit.Run(t, repo, "commit", "-q", "-m", "one")
	h := &hookProject{t: t, repo: repo, db: filepath.Join(t.TempDir(), "at.db")}
	if code, out := h.run(repo, "", "init", "--name", "hooks"); code != ExitOK {
		t.Fatalf("init: %d %s", code, out)
	}
	code, out := h.run(repo, "", "--json", "add", "hook-work", "--proof", "run: go version", "--regression", "run: go version")
	if code != ExitOK {
		t.Fatalf("add: %d %s", code, out)
	}
	var added struct {
		Result struct{ Task struct{ ID string } }
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil {
		t.Fatal(err)
	}
	h.id = added.Result.Task.ID
	code, out = h.run(repo, "", "--json", "claim", h.id)
	if code != ExitOK {
		t.Fatalf("claim: %d %s", code, out)
	}
	var claimed struct {
		Result struct{ Token, Workspace string }
	}
	if err := json.Unmarshal([]byte(out), &claimed); err != nil {
		t.Fatal(err)
	}
	h.ws, h.tok = claimed.Result.Workspace, claimed.Result.Token
	return h
}

func TestContextHookIsDeterministicAndMinimal(t *testing.T) {
	h := newHookProject(t)
	if code, out := h.run(h.ws, "", "log", "--done", "wrote parser", "--next", "add tests", "--learned", "yaml is stable"); code != ExitOK {
		t.Fatalf("log: %d %s", code, out)
	}

	code, first := h.run(h.ws, "", "context", "--hook")
	if code != ExitOK {
		t.Fatalf("hook: %d %s", code, first)
	}
	_, second := h.run(h.ws, "", "context", "--hook")
	if first != second {
		t.Fatalf("not deterministic:\n%s\n---\n%s", first, second)
	}
	if strings.Contains(first, h.tok) || regexp.MustCompile(`\d{4}-\d\d-\d\dT`).MatchString(first) {
		t.Fatalf("output leaks token or timestamp:\n%s", first)
	}
	order := []string{"atask_context_version: 1", "task:", "objective:", "acceptance:", "progress:", "blockers:", "verification:", "workspace:"}
	at := -1
	for _, key := range order {
		i := strings.Index(first, "\n"+key)
		if key == order[0] {
			i = strings.Index(first, key)
		}
		if i <= at {
			t.Fatalf("%q out of order in:\n%s", key, first)
		}
		at = i
	}
	for _, want := range []string{"id: " + h.id, "wrote parser", "next: add tests", "yaml is stable", "proof: not_run", "path: " + h.ws} {
		if !strings.Contains(first, want) {
			t.Errorf("missing %q in:\n%s", want, first)
		}
	}

	t.Run("no claim", func(t *testing.T) {
		code, out := h.run(t.TempDir(), "", "context", "--hook")
		if code != ExitOK || out != noClaimHookOutput {
			t.Fatalf("got %d %q", code, out)
		}
	})
	t.Run("hook with id is usage error", func(t *testing.T) {
		if code, _ := h.run(h.ws, "", "context", "--hook", h.id); code == ExitOK {
			t.Fatal("expected usage error")
		}
	})
}

func TestContextHookReportsCompleteTask(t *testing.T) {
	h := newHookProject(t)
	testgit.Run(t, h.ws, "commit", "-q", "--allow-empty", "-m", "work")
	if code, out := h.run(h.ws, "", "verify", "complete"); code != ExitOK {
		t.Fatalf("complete: %d %s", code, out)
	}
	_, out := h.run(h.ws, "", "context", "--hook")
	if !strings.Contains(out, "status: complete") || strings.Contains(out, "progress:") {
		t.Fatalf("complete form wrong:\n%s", out)
	}
}

func TestContextHookChangedEmitsOnlyOnStateChange(t *testing.T) {
	h := newHookProject(t)
	_, first := h.run(h.ws, hookSessionPayload, "context", "--hook", "--changed")
	if !strings.Contains(first, "id: "+h.id) {
		t.Fatalf("first run printed nothing useful: %q", first)
	}
	if _, second := h.run(h.ws, hookSessionPayload, "context", "--hook", "--changed"); second != "" {
		t.Fatalf("unchanged state printed: %q", second)
	}
	h.run(h.ws, "", "log", "--next", "something new")
	if _, third := h.run(h.ws, hookSessionPayload, "context", "--hook", "--changed"); !strings.Contains(third, "something new") {
		t.Fatalf("changed state not printed: %q", third)
	}
	if _, plain := h.run(h.ws, "", "context", "--hook", "--changed"); !strings.Contains(plain, "something new") {
		t.Fatalf("no session id must behave as plain --hook: %q", plain)
	}
}
