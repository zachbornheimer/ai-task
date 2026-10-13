package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneralProjectIsDefault(t *testing.T) {
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

	code, out, errOut := run("add", "loose work", "--check", "u: true", "--json")
	if code != ExitOK {
		t.Fatalf("add: %d %s", code, errOut)
	}
	if !strings.Contains(out, `"project": "general"`) && !strings.Contains(out, `"project":"general"`) {
		t.Fatalf("task must land in project general:\n%s", out)
	}

	code, out, errOut = run("projects", "--json")
	if code != ExitOK {
		t.Fatalf("projects: %d %s", code, errOut)
	}
	var rows []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Path string `json:"root_path"`
	}
	if err := json.Unmarshal([]byte(unwrapResult(out)), &rows); err != nil {
		t.Fatalf("decode projects: %v\n%s", err, out)
	}
	sawGeneral := false
	for _, r := range rows {
		if r.Name == "" || r.ID == "" {
			t.Fatalf("blank project row %+v in:\n%s", r, out)
		}
		if r.Name == "general" {
			sawGeneral = true
			if r.Path != "" {
				t.Fatalf("general must have no path: %+v", r)
			}
		}
	}
	if !sawGeneral {
		t.Fatalf("projects must list general:\n%s", out)
	}

	for _, args := range [][]string{{"--project", "general", "list"}, {"list"}} {
		code, out, errOut = run(args...)
		if code != ExitOK || !strings.Contains(out, "loose work") {
			t.Fatalf("%v: %d %s %s", args, code, out, errOut)
		}
	}
}

// unwrapResult returns the "result" member of an {"ok":..,"result":..}
// envelope, or the input when it is not wrapped.
func unwrapResult(s string) string {
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal([]byte(s), &env) == nil && len(env.Result) > 0 {
		return string(env.Result)
	}
	return s
}
