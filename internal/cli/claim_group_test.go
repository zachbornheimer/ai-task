package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestClaimGroupFlag: `claim --group` claims the next member of a group
// and answers NOT_READY (with the contract's details), DONE, and
// INVALID_INPUT as documented.
func TestClaimGroupFlag(t *testing.T) {
	db, cwd := filepath.Join(t.TempDir(), "at.db"), t.TempDir()
	run := func(args ...string) (int, []byte) {
		t.Helper()
		var out, errb bytes.Buffer
		code := Main(Env{Args: append([]string{"--json"}, args...), Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Cwd: cwd, Getenv: func(k string) string {
			if k == "ATASK_DB" {
				return db
			}
			return ""
		}})
		return code, out.Bytes()
	}
	type envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Token string `json:"token"`
			Task  struct {
				ID string `json:"id"`
			} `json:"task"`
		} `json:"result"`
		Error *struct {
			Code    string `json:"code"`
			Details struct {
				Group              string `json:"group"`
				GroupProofsPending *bool  `json:"group_proofs_pending"`
				Active             *int   `json:"active"`
			} `json:"details"`
		} `json:"error"`
	}
	do := func(wantExit int, args ...string) envelope {
		t.Helper()
		code, out := run(args...)
		var env envelope
		if err := json.Unmarshal(out, &env); err != nil {
			t.Fatalf("%v: not an envelope: %v\n%s", args, err, out)
		}
		if code != wantExit {
			t.Fatalf("%v: exit %d, want %d\n%s", args, code, wantExit, out)
		}
		return env
	}
	added := func(args ...string) string {
		t.Helper()
		var env struct {
			Result struct {
				Task struct {
					ID string `json:"id"`
				} `json:"task"`
				Created map[string]string `json:"created"`
			} `json:"result"`
		}
		code, out := run(args...)
		if code != ExitOK || json.Unmarshal(out, &env) != nil {
			t.Fatalf("%v: exit %d\n%s", args, code, out)
		}
		if env.Result.Task.ID != "" {
			return env.Result.Task.ID
		}
		for _, id := range env.Result.Created {
			return id
		}
		t.Fatalf("%v: no id in %s", args, out)
		return ""
	}

	g := added("add", "G", "--group", "--key", "g")
	outside := added("add", "outside", "--proof", "run: go version", "--regression", "run: go version")
	member := added("add", "member", "--parent", g, "--proof", "run: go version", "--regression", "run: go version")

	claimed := do(ExitOK, "claim", "--group", g)
	if !claimed.OK || claimed.Result.Task.ID != member || claimed.Result.Token == "" {
		t.Fatalf("claim --group %s = %+v, want the member %s (not %s)", g, claimed, member, outside)
	}
	// The key resolves like any reference.
	waiting := do(ExitError, "claim", "--group", "g")
	if waiting.Error == nil || waiting.Error.Code != "NOT_READY" || waiting.Error.Details.Group != g ||
		waiting.Error.Details.GroupProofsPending == nil || *waiting.Error.Details.GroupProofsPending ||
		waiting.Error.Details.Active == nil || *waiting.Error.Details.Active != 1 {
		t.Fatalf("NOT_READY envelope = %+v", waiting.Error)
	}
	for _, args := range [][]string{{"claim", "--group", g, member}, {"claim", "--group", member}} {
		if env := do(ExitError, args...); env.Error == nil || env.Error.Code != "INVALID_INPUT" {
			t.Fatalf("%v: %+v, want INVALID_INPUT", args, env.Error)
		}
	}
	if done := do(ExitOK, "verify", "complete", claimed.Result.Token); !done.OK {
		t.Fatalf("verify complete: %+v", done)
	}
	if env := do(ExitError, "claim", "--group", g); env.Error == nil || env.Error.Code != "DONE" {
		t.Fatalf("finished group: %+v, want DONE", env.Error)
	}
}
