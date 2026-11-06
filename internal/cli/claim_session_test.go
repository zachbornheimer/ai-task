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

// A task claimed from the repository root is worked in its own worktree, and
// `verify` run from the root again, with no token anywhere, must find the
// session the claim left: the help names "the directory you claimed from" as
// a place the token resolves.
func TestClaimFromRepoRootLeavesASessionTheDirectoryResolves(t *testing.T) {
	repo := t.TempDir()
	testgit.Run(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	testgit.Run(t, repo, "add", "a.txt")
	testgit.Run(t, repo, "commit", "-q", "-m", "one")
	db := filepath.Join(t.TempDir(), "at.db")

	run := func(args ...string) (int, string) {
		t.Helper()
		var out, errb bytes.Buffer
		getenv := func(k string) string {
			if k == "ATASK_DB" {
				return db
			}
			return ""
		}
		code := Main(Env{Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Cwd: repo, Getenv: getenv})
		if code == ExitOK {
			// A slow claim warns on stderr; JSON callers read stdout alone.
			return code, out.String()
		}
		return code, out.String() + errb.String()
	}

	if code, out := run("init", "--name", "rootsession"); code != ExitOK {
		t.Fatalf("init: %d %s", code, out)
	}
	code, out := run("--json", "add", "root-work", "--proof", "run: test -f work.txt", "--regression", "run: true")
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
	code, out = run("--json", "claim", added.Result.Task.ID)
	if code != ExitOK {
		t.Fatalf("claim: %d %s", code, out)
	}
	var claimed struct {
		Result struct {
			Workspace string `json:"workspace"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &claimed); err != nil || claimed.Result.Workspace == "" {
		t.Fatalf("claim json: %v %s", err, out)
	}

	if code, out := run("log", "--done", "started"); code != ExitOK {
		t.Fatalf("log from the claiming directory: %d %s", code, out)
	}
	ws := claimed.Result.Workspace
	if err := os.WriteFile(filepath.Join(ws, "work.txt"), []byte("work"), 0o600); err != nil {
		t.Fatal(err)
	}
	testgit.Run(t, ws, "add", "work.txt")
	testgit.Run(t, ws, "commit", "-q", "-m", "work")

	code, out = run("--json", "verify", "complete")
	if code != ExitOK {
		t.Fatalf("verify complete from the claiming directory: %d %s", code, out)
	}
	var verified struct {
		Result struct {
			Completed bool `json:"completed"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &verified); err != nil || !verified.Result.Completed {
		t.Fatalf("verify complete did not complete the task: %v %s", err, out)
	}
}
