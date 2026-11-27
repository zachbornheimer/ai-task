package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeStaleContextNeverFallsBackToLegacyClaim(t *testing.T) {
	db, cwd := filepath.Join(t.TempDir(), "at.db"), t.TempDir()
	root, _, registration := runtimeFixture(t)
	run := func(runtimeRoot, session, token string, args ...string) (int, string, string) {
		var out, errout bytes.Buffer
		code := Main(Env{Args: args, Cwd: cwd, Stdout: &out, Stderr: &errout, Stdin: strings.NewReader(""), Getenv: func(key string) string {
			switch key {
			case "ATASK_DB":
				return db
			case "ATASK_RUNTIME_ROOT":
				return runtimeRoot
			case "PI_SESSION_ID":
				return session
			case "ATASK_SESSION":
				return token
			}
			return ""
		}})
		return code, out.String(), errout.String()
	}
	legacy := func(args ...string) (int, string, string) { return run("", "", "", args...) }
	code, out, errout := addClaimed(t, legacy, "legacy owner", "--proof", "run: go version", "--regression", "run: go version")
	if code != 0 {
		t.Fatalf("claim: %d %s %s", code, out, errout)
	}
	var claim struct {
		Result struct {
			Session struct {
				Token string `json:"token"`
			} `json:"session"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &claim); err != nil || claim.Result.Session.Token == "" {
		t.Fatalf("claim token missing: %v", err)
	}
	token := claim.Result.Session.Token
	cases := []struct{ name, root, session string }{
		{"missing root", filepath.Join(root, "absent"), registration.Session},
		{"missing session", root, ""},
		{"missing registration", root, "unregistered"},
		{"valid foreign runtime", root, registration.Session},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code, out, _ := run(tc.root, tc.session, "", "log", "--done", "must not select legacy"); code == 0 {
				t.Fatalf("stale context selected legacy claim: %s", out)
			}
			if code, out, errout := run(tc.root, tc.session, token, "log", "--done", "explicit environment authority"); code != 0 {
				t.Fatalf("explicit authority lost: %d %s %s", code, out, errout)
			}
		})
	}
	if code, out, errout := legacy("log", "--done", "legacy compatibility"); code != 0 {
		t.Fatalf("legacy: %s %s", out, errout)
	}
}
