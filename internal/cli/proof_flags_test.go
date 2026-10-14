package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

type proofHarness struct {
	t   *testing.T
	db  string
	cwd string
}

func newProofHarness(t *testing.T) *proofHarness {
	t.Helper()
	h := &proofHarness{t: t, db: filepath.Join(t.TempDir(), "at.db"), cwd: t.TempDir()}
	if code, _, errOut := h.run("init", "--name", "proofs"); code != ExitOK {
		t.Fatalf("init: %d %s", code, errOut)
	}
	return h
}

func (h *proofHarness) run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Main(Env{Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Cwd: h.cwd, Getenv: func(k string) string {
		if k == "ATASK_DB" {
			return h.db
		}
		return ""
	}})
	return code, out.String(), errb.String()
}

// failsWith asserts the call exits non-zero with INVALID_INPUT and a message
// containing every want.
func (h *proofHarness) failsWith(want []string, args ...string) {
	h.t.Helper()
	code, out, errOut := h.run(append([]string{"--json"}, args...)...)
	if code == ExitOK {
		h.t.Fatalf("%v: expected failure, got success:\n%s", args, out)
	}
	all := out + errOut
	for _, w := range append([]string{"INVALID_INPUT"}, want...) {
		if !strings.Contains(all, w) {
			h.t.Fatalf("%v: output lacks %q:\n%s", args, w, all)
		}
	}
}

func TestAddRequiresBothProofs(t *testing.T) {
	h := newProofHarness(t)
	for _, args := range [][]string{{"add", "t1", "--proof", "run: go version"}, {"add", "t2", "--regression", "run: go version"}} {
		if code, out, _ := h.run(append([]string{"--json"}, args...)...); code != ExitOK || !strings.Contains(out, `"status":"draft"`) {
			t.Fatalf("%v must file a draft: %d %s", args, code, out)
		}
	}
	h.failsWith([]string{"--proof"}, "add", "t", "--check", "x: y")

	if code, _, errOut := h.run("add", "ok", "--key", "ok", "--proof", "run: go version", "--regression", "run: go version", "--size", "small"); code != ExitOK {
		t.Fatalf("valid add: %d %s", code, errOut)
	}
	code, out, errOut := h.run("show", "ok")
	if code != ExitOK {
		t.Fatalf("show: %d %s", code, errOut)
	}
	for _, want := range []string{"TASK CHECKS", "proof", "regression", "run: go version"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "run: go version") != 2 {
		t.Fatalf("show should list both specs verbatim:\n%s", out)
	}

	code, out, errOut = h.run("--json", "show", "ok")
	if code != ExitOK {
		t.Fatalf("show --json: %d %s", code, errOut)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	res, _ := raw["result"].(map[string]any)
	for _, k := range []string{"proof", "regression"} {
		obj, ok := res[k].(map[string]any)
		if !ok {
			t.Fatalf("show --json lacks %q object:\n%s", k, out)
		}
		for _, f := range []string{"preset", "subject", "options", "size"} {
			if _, ok := obj[f]; !ok {
				t.Fatalf("%s lacks %q: %v", k, f, obj)
			}
		}
		if obj["preset"] != "run" || obj["subject"] != "go version" {
			t.Fatalf("%s = %v", k, obj)
		}
	}

	h.failsWith([]string{"proof"}, "verify", "task")
	if code, _, errOut := h.run("verify", "proof"); strings.Contains(errOut, "verify needs a mode") {
		t.Fatalf("verify proof rejected as a mode: %d %s", code, errOut)
	}
}
