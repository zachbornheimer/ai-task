package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/testgit"
)

// TestLogAcceptsClaimToken: the token in the claim JSON works for `log`
// through every documented path, and a stray positional never reports a
// malformed session token.
func TestLogAcceptsClaimToken(t *testing.T) {
	repo := t.TempDir()
	testgit.Run(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	testgit.Run(t, repo, "add", "a.txt")
	testgit.Run(t, repo, "commit", "-q", "-m", "one")
	db := filepath.Join(t.TempDir(), "at.db")

	run := func(cwd, session, stdin string, args ...string) (int, string) {
		t.Helper()
		var out, errb bytes.Buffer
		getenv := func(k string) string {
			switch k {
			case "ATASK_DB":
				return db
			case "ATASK_SESSION":
				return session
			}
			return ""
		}
		code := Main(Env{Args: args, Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb, Cwd: cwd, Getenv: getenv})
		return code, out.String() + errb.String()
	}

	if code, out := run(repo, "", "", "init", "--name", "tokens"); code != ExitOK {
		t.Fatalf("init: %d %s", code, out)
	}
	code, out := run(repo, "", "", "--json", "add", "token-work", "--proof", "run: go version", "--regression", "run: go version")
	if code != ExitOK {
		t.Fatalf("add: %d %s", code, out)
	}
	var added struct {
		Result struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil || added.Result.Task.ID == "" {
		t.Fatalf("add json: %v %s", err, out)
	}
	id := added.Result.Task.ID

	code, out = run(repo, "", "", "--json", "claim", id)
	if code != ExitOK {
		t.Fatalf("claim: %d %s", code, out)
	}
	var claimed struct {
		Result struct {
			Token     string `json:"token"`
			Workspace string `json:"workspace"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &claimed); err != nil || claimed.Result.Token == "" || claimed.Result.Workspace == "" {
		t.Fatalf("claim json: %v %s", err, out)
	}
	token, ws := claimed.Result.Token, claimed.Result.Workspace
	other := t.TempDir()

	cases := []struct {
		name, cwd, session, stdin string
		args                      []string
	}{
		{"env from another cwd", other, token, "", []string{"log", "--done", "x"}},
		{"worktree, no env", ws, "", "", []string{"log", "--done", "x"}},
		{"positional token", other, "", "", []string{"log", token, "--done", "x"}},
		{"stdin token", other, "", token + "\n", []string{"log", "-", "--done", "x"}},
		{"json env", other, token, "", []string{"--json", "log", "--done", "x"}},
	}
	for _, tc := range cases {
		if code, out := run(tc.cwd, tc.session, tc.stdin, tc.args...); code != ExitOK {
			t.Errorf("%s: exit %d %s", tc.name, code, out)
		}
	}

	// A stray positional (task id, or unquoted words) never reads as a token.
	for _, args := range [][]string{{"log", id, "--done", "x"}, {"log", "--done", "two", "words"}} {
		code, out := run(other, token, "", args...)
		if strings.Contains(out, "malformed session token") {
			t.Errorf("%v: reported a malformed token: %s", args, out)
		}
		if code == ExitOK {
			t.Errorf("%v: stray positional accepted", args)
		}
		if !strings.Contains(out, "quote") || !strings.Contains(out, "ATASK_SESSION") {
			t.Errorf("%v: message does not name the right usage: %s", args, out)
		}
	}
}
