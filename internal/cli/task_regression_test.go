package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskRequiresRegressionAndCompletion(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	cwd := t.TempDir()
	getenv := func(k string) string {
		if k == "ATASK_DB" {
			return db
		}
		return ""
	}
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := Main(Env{Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Cwd: cwd, Getenv: getenv})
		return code, out.String(), errb.String()
	}
	code, _, errOut := run("add", "only-complete", "--check", "u: true")
	if code == ExitOK || !strings.Contains(errOut, "MISSING_VERIFICATION") {
		t.Fatalf("add without regression: %d %s", code, errOut)
	}
	code, _, errOut = run("add", "only-reg", "--regression-check", "r: true")
	if code == ExitOK || !strings.Contains(errOut, "MISSING_VERIFICATION") {
		t.Fatalf("add without completion: %d %s", code, errOut)
	}
	code, out, errOut := run("--json", "add", "both-gates", "--check", "u: true", "--regression-check", "r: true", "--regression-size", "medium")
	if code != ExitOK {
		t.Fatalf("add both: %d %s", code, errOut)
	}
	var env struct {
		Result struct {
			Task struct {
				ID           string `json:"id"`
				TaskChecks   []struct{ ID string } `json:"task_checks"`
				Verification struct {
					Regression []struct {
						ID   string `json:"id"`
						Size string `json:"size"`
					} `json:"regression"`
				} `json:"verification"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("json: %v %s", err, out)
	}
	if env.Result.Task.ID == "" {
		t.Fatalf("missing id: %s", out)
	}
}
