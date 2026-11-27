package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeRenewWithoutRegisteredOwnerReturnsSafeZero(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	cwd := t.TempDir()
	run := func(args ...string) (int, string, string) {
		var out, errout bytes.Buffer
		code := Main(Env{Args: args, Cwd: cwd, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errout, Getenv: func(key string) string {
			if key == "ATASK_DB" {
				return db
			}
			return ""
		}})
		return code, out.String(), errout.String()
	}
	code, out, errout := addClaimed(t, run, "legacy rootless task", "--proof", "run: go version", "--regression", "run: go version")
	if code != ExitOK {
		t.Fatalf("claim: %d %s %s", code, out, errout)
	}
	code, out, errout = run("claim", "renew", "--runtime", "--json")
	if code != ExitOK {
		t.Fatalf("renew runtime: %d %s %s", code, out, errout)
	}
	var envelope struct {
		OK     bool           `json:"ok"`
		Result map[string]int `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil || !envelope.OK || len(envelope.Result) != 1 || envelope.Result["renewed"] != 0 {
		t.Fatalf("unsafe runtime result %s error %v", out, err)
	}
	for _, args := range [][]string{{"claim", "release", "--runtime"}, {"claim", "renew", "--runtime", "extra"}} {
		if code, out, _ := run(args...); code == ExitOK {
			t.Fatalf("invalid runtime invocation %v succeeded: %s", args, out)
		}
	}
}
