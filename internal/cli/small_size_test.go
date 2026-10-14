package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestSmallSizeAddCLIRejectsUnboundedGoTest(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	cwd := t.TempDir()
	getenv := func(k string) string {
		if k == "ATASK_DB" {
			return db
		}
		return ""
	}
	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := Main(Env{Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Cwd: cwd, Getenv: getenv})
		return code, errb.String()
	}
	code, errOut := run("add", "wide unit", "--proof", "run: go test ./internal/cli -count=1", "--regression", "run: true")
	if code == ExitOK {
		t.Fatalf("unbounded go test on small must be rejected: %s", errOut)
	}
	if !strings.Contains(errOut, "-run") || !strings.Contains(errOut, "size=") {
		t.Fatalf("reject must name -run and size=:\n%s", errOut)
	}
	if code, errOut = run("add", "narrow unit", "--proof", "run: go test ./internal/cli -count=1 -run TestSmallSize", "--regression", "run: true"); code != ExitOK {
		t.Fatalf("narrowed go test: %d %s", code, errOut)
	}
	if code, errOut = run("add", "medium unit", "--proof", "run: go test ./internal/cli -count=1 size=medium", "--regression", "run: true"); code != ExitOK {
		t.Fatalf("medium unbounded: %d %s", code, errOut)
	}
}
