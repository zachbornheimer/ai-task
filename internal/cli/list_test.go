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
	code, out, errOut := run(alpha, "projects")
	if code != ExitOK {
		t.Fatalf("projects: %d %s", code, errOut)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Fatalf("projects should name every project:\n%s", out)
	}
	if code, _, errOut := run(alpha, "add", "from-alpha", "--check", "u: true", "--regression-check", "r: true"); code != ExitOK {
		t.Fatalf("add: %d %s", code, errOut)
	}
	code, out, errOut = run(alpha, "list")
	if code != ExitOK {
		t.Fatalf("list: %d %s", code, errOut)
	}
	if !strings.Contains(out, "from-alpha") {
		t.Fatalf("bare list is the global board:\n%s", out)
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
	if !strings.Contains(out, "no tasks registered in beta") {
		t.Fatalf("empty project should say so:\n%s", out)
	}
}

func TestListEmptyDatabase(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	var o, e bytes.Buffer
	code := Main(Env{Args: []string{"list"}, Stdin: strings.NewReader(""), Stdout: &o, Stderr: &e, Cwd: t.TempDir(), Getenv: func(k string) string {
		if k == "ATASK_DB" {
			return db
		}
		return ""
	}})
	if code != ExitOK {
		t.Fatalf("list: %d %s", code, e.String())
	}
	if !strings.Contains(o.String(), "no tasks registered") {
		t.Fatalf("empty db should show the board, not NO_PROJECT:\n%s", o.String())
	}
	if strings.Contains(o.String()+e.String(), "NO_PROJECT") {
		t.Fatalf("NO_PROJECT leaked:\n%s %s", o.String(), e.String())
	}
}

func TestListFromUnregisteredDirIsInboxTasks(t *testing.T) {
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
	if code, _, errOut := run("add", "file the tracker", "--check", "u: true", "--regression-check", "r: true"); code != ExitOK {
		t.Fatalf("add from unregistered dir: %d %s", code, errOut)
	}
	code, out, errOut := run("list")
	if code != ExitOK {
		t.Fatalf("list: %d %s", code, errOut)
	}
	if strings.Contains(out, "NO_PROJECT") || strings.Contains(errOut, "NO_PROJECT") {
		t.Fatalf("list from unregistered dir: %s %s", out, errOut)
	}
	if !strings.Contains(out, "file the tracker") {
		t.Fatalf("list should show inbox tasks:\n%s", out)
	}
}

func TestAddSameOpenTitleIsNoOp(t *testing.T) {
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
	code, first, errOut := run("add", "rename host loops", "--check", "u: true", "--regression-check", "r: true")
	if code != ExitOK {
		t.Fatalf("first add: %d %s", code, errOut)
	}
	code, second, errOut := run("add", "rename host loops", "--check", "u: true", "--regression-check", "r: true")
	if code != ExitOK {
		t.Fatalf("second add: %d %s", code, errOut)
	}
	if !strings.Contains(second, "Unchanged") {
		t.Fatalf("second add must be Unchanged:\n%s", second)
	}
	fields := strings.Fields(first)
	var id string
	for _, f := range fields {
		if strings.HasPrefix(f, "at-") {
			id = f
			break
		}
	}
	if id == "" || !strings.Contains(second, id) {
		t.Fatalf("second add must return %s:\nfirst=%s\nsecond=%s", id, first, second)
	}
}
