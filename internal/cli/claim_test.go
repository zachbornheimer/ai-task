package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONClaimRenewDispatch(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	cwd := t.TempDir()
	getenv := func(k string) string {
		if k == "ATASK_DB" {
			return db
		}
		return ""
	}
	run := func(args ...string) (int, string, string) {
		t.Helper()
		var out, errb bytes.Buffer
		code := Main(Env{Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Cwd: cwd, Getenv: getenv})
		return code, out.String(), errb.String()
	}

	// A task titled "renew" must not be claimed when the verb is the
	// subcommand: global --json must not turn `claim renew` into a title lookup.
	if code, _, errOut := run("--json", "add", "renew", "--proof", "run: go version", "--regression", "run: go version"); code != ExitOK {
		t.Fatalf("add renew: %d %s", code, errOut)
	}
	code, out, errOut := addClaimed(t, run, "held-work", "--proof", "run: go version", "--regression", "run: go version")
	if code != ExitOK {
		t.Fatalf("add --claim: %d %s", code, errOut)
	}
	var claimed struct {
		Result struct {
			Session struct {
				Token string `json:"token"`
			} `json:"session"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &claimed); err != nil || claimed.Result.Session.Token == "" {
		t.Fatalf("claim json: %v %s", err, out)
	}

	for _, args := range [][]string{
		{"--json", "claim", "renew", claimed.Result.Session.Token},
		{"claim", "--json", "renew", claimed.Result.Session.Token},
		{"claim", "renew", "--json", claimed.Result.Session.Token},
	} {
		code, out, errOut = run(args...)
		if code != ExitOK {
			t.Fatalf("%v: exit %d %s %s", args, code, out, errOut)
		}
		if !isRenewEnvelope(out) {
			t.Fatalf("%v: want renew envelope with only lease_until, got %s", args, out)
		}
	}

	code, out, errOut = run("--json", "claim", "renew")
	if code == ExitOK {
		t.Fatalf("claim renew without a token must not claim the task named renew:\n%s", out)
	}
	if !strings.Contains(out, "session token required") {
		t.Fatalf("claim renew without a token: %d %s %s", code, out, errOut)
	}
}

func isRenewEnvelope(out string) bool {
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || !env.OK {
		return false
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(env.Result, &keys); err != nil {
		return false
	}
	_, hasLease := keys["lease_until"]
	_, hasTask := keys["task"]
	return hasLease && !hasTask
}
