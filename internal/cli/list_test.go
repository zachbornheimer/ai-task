package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestListAllProjectsThenOne(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	alpha := t.TempDir()
	beta := t.TempDir()
	getenv := func(k string) string {
		if k == "ATASK_DB" {
			return db
		}
		return ""
	}
	run := func(cwd string, args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := Main(Env{Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Cwd: cwd, Getenv: getenv})
		return code, out.String(), errb.String()
	}
	if code, _, errOut := run(alpha, "init", "--name", "alpha", "--no-instructions"); code != ExitOK {
		t.Fatalf("init alpha: %d %s", code, errOut)
	}
	if code, _, errOut := run(beta, "init", "--name", "beta", "--no-instructions"); code != ExitOK {
		t.Fatalf("init beta: %d %s", code, errOut)
	}
	code, out, errOut := run(alpha, "list")
	if code != ExitOK {
		t.Fatalf("list: %d %s", code, errOut)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Fatalf("list should name every project:\n%s", out)
	}
	if code, _, errOut := run(alpha, "add", "from-alpha", "--check", "u: true"); code != ExitOK {
		t.Fatalf("add: %d %s", code, errOut)
	}
	code, out, errOut = run(beta, "list", "alpha")
	if code != ExitOK {
		t.Fatalf("list alpha: %d %s", code, errOut)
	}
	if !strings.Contains(out, "from-alpha") {
		t.Fatalf("list alpha should show that project's tasks:\n%s", out)
	}
	code, out, errOut = run(alpha, "list", "beta")
	if code != ExitOK {
		t.Fatalf("list beta: %d %s", code, errOut)
	}
	if strings.Contains(out, "from-alpha") {
		t.Fatalf("list beta should not show alpha's tasks:\n%s", out)
	}
}
