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
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := Main(Env{Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Cwd: cwd, Getenv: getenv})
		return code, out.String(), errb.String()
	}
	code, _, errOut := run("add", "wide unit", "--check", "unit: go test ./internal/cli -count=1")
	if code == ExitOK {
		t.Fatalf("unbounded go test on small must be rejected: %s", errOut)
	}
	if !strings.Contains(errOut, "-run") || !strings.Contains(errOut, "--size") {
		t.Fatalf("reject must name -run and --size:\n%s", errOut)
	}
	if code, _, errOut = run("add", "narrow unit", "--check", "unit: go test ./internal/cli -count=1 -run TestSmallSize"); code != ExitOK {
		t.Fatalf("narrowed go test: %d %s", code, errOut)
	}
	if code, _, errOut = run("add", "medium unit", "--size", "medium", "--check", "unit: go test ./internal/cli -count=1"); code != ExitOK {
		t.Fatalf("medium unbounded: %d %s", code, errOut)
	}
}
