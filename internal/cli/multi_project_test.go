package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddWithRepeatedProjectFlag(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := Main(Env{Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Cwd: t.TempDir(), Getenv: func(k string) string {
			if k == "ATASK_DB" {
				return db
			}
			return ""
		}})
		return code, out.String(), errb.String()
	}
	for _, name := range []string{"alpha", "beta"} {
		if code, _, e := run("project", "add", name); code != ExitOK {
			t.Fatalf("project add %s: %d %s", name, code, e)
		}
	}
	code, out, e := run("--json", "add", "shared", "--project", "alpha", "--project", "beta", "--proof", "run: go version", "--regression", "run: go version")
	if code != ExitOK || !strings.Contains(out, `"project":"alpha"`) || !strings.Contains(out, `"projects":["alpha","beta"]`) {
		t.Fatalf("add: %d %s %s", code, out, e)
	}
	if code, out, _ := run("list", "--project", "beta"); code != ExitOK || !strings.Contains(out, "shared") {
		t.Fatalf("list --project beta must include the task:\n%s", out)
	}
}
