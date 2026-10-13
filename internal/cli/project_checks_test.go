package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectHoldsNoChecks(t *testing.T) {
	h := newProofHarness(t)

	for _, flag := range [][]string{
		{"--regression-check", "x: true"},
		{"--regression-json", `[]`},
		{"--regression-file", "x.json"},
		{"--integration", "promote"},
		{"--regression-budget", "5m"},
		{"--budget-small", "1m"},
		{"--budget-medium", "1m"},
		{"--budget-large", "1m"},
	} {
		h.failsWith([]string{"removed"}, append([]string{"project"}, flag...)...)
	}
	h.failsWith([]string{"--regression"}, "project", "--regression-check", "x: true")

	code, out, errOut := h.run("--json", "project")
	if code != ExitOK {
		t.Fatalf("project: %d %s", code, errOut)
	}
	if fresh := projectJSON(t, out); fresh["attempts"] != float64(3) {
		t.Fatalf("new project attempts = %v, want 3", fresh["attempts"])
	}

	if code, _, errOut := h.run("project", "--target-branch", "main", "--attempts", "4"); code != ExitOK {
		t.Fatalf("project --target-branch --attempts: %d %s", code, errOut)
	}
	_, out, _ = h.run("--json", "project")
	fields := projectJSON(t, out)
	if len(fields) != 5 {
		t.Fatalf("project json keys = %v, want exactly name, id, path, target_branch, attempts", fields)
	}
	for _, k := range []string{"name", "id", "path", "target_branch", "attempts"} {
		if _, ok := fields[k]; !ok {
			t.Fatalf("project json lacks %q: %v", k, fields)
		}
	}
	if fields["target_branch"] != "main" || fields["attempts"] != float64(4) {
		t.Fatalf("project json = %v", fields)
	}

	_, out, errOut = h.run("--json", "doctor")
	if strings.Contains(out+errOut, `"baseline"`) || strings.Contains(out+errOut, "regression_checks") {
		t.Fatalf("doctor still reports a project regression baseline:\n%s", out)
	}

	assertVerifyRunsTaskRegression(t)
}

func projectJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	var env struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return env.Result
}

// The task's own regression proof is what `verify complete` runs, in the
// task's workspace; a failing one stops completion.
func assertVerifyRunsTaskRegression(t *testing.T) {
	h := &proofHarness{t: t, db: filepath.Join(t.TempDir(), "at.db"), cwd: t.TempDir()}
	claim := func(title, regression string) string {
		t.Helper()
		code, out, errOut := h.run("--json", "add", title, "--proof", "run: test -d .", "--regression", regression, "--claim")
		if code != ExitOK {
			t.Fatalf("add %s: %d %s %s", title, code, out, errOut)
		}
		var env struct {
			Result struct {
				Session struct{ Token string } `json:"session"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("decode %q: %v", out, err)
		}
		return env.Result.Session.Token
	}
	complete := func(token string) (int, string) {
		t.Helper()
		var out, errb bytes.Buffer
		code := Main(Env{Args: []string{"--json", "verify", "complete"}, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Cwd: h.cwd, Getenv: func(k string) string {
			switch k {
			case "ATASK_DB":
				return h.db
			case "ATASK_SESSION":
				return token
			}
			return ""
		}})
		return code, out.String() + errb.String()
	}

	if code, out := complete(claim("writes", "run: sh -c 'echo reg > reg.txt'")); code != ExitOK {
		t.Fatalf("complete with passing regression: %d %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(h.cwd, "reg.txt")); err != nil {
		t.Fatalf("regression proof did not run in the workspace: %v", err)
	}
	if code, out := complete(claim("fails", "run: false")); code == ExitOK {
		t.Fatalf("a failing regression must not complete the task: %s", out)
	}
}
