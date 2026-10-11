package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitInitDir(t *testing.T, dir string) {
	t.Helper()
	cmd := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "HK=0",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	cmd("init", "-q", "-b", "main")
	cmd("config", "user.name", "t")
	cmd("config", "user.email", "t@t")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd("add", "f.txt")
	cmd("commit", "-q", "-m", "init")
}

func TestListAllProjectsThenOne(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	alpha := t.TempDir()
	beta := t.TempDir()
	gitInitDir(t, alpha)
	gitInitDir(t, beta)
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
