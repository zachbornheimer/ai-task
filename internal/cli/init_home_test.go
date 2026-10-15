package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runInitAt(t *testing.T, cwd string, args ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	db := filepath.Join(t.TempDir(), "at.db")
	code := Main(Env{Args: append([]string{"--json", "init"}, args...), Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Cwd: cwd, Getenv: func(k string) string {
		if k == "ATASK_DB" {
			return db
		}
		return ""
	}})
	return code, out.String() + errb.String()
}

func TestInitRefusesHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := map[string]struct {
		cwd  string
		args []string
		dir  string
	}{
		"cwd is home":             {cwd: home, dir: home},
		"path is home":            {cwd: t.TempDir(), args: []string{"--path", home}, dir: home},
		"path is home dir parent": {cwd: t.TempDir(), args: []string{"--path", filepath.Dir(home)}, dir: filepath.Dir(home)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			code, out := runInitAt(t, tc.cwd, tc.args...)
			want := "refusing to register " + tc.dir + ": use a project directory or atask project add NAME for work without a repository"
			if code == ExitOK || !strings.Contains(out, "INVALID_INPUT") || !strings.Contains(out, want) {
				t.Fatalf("exit %d, want INVALID_INPUT %q\n%s", code, want, out)
			}
			if _, err := os.Stat(filepath.Join(tc.dir, ".git")); err == nil {
				t.Fatalf(".git created in %s", tc.dir)
			}
		})
	}
	t.Run("child registers", func(t *testing.T) {
		child := filepath.Join(home, "proj")
		if err := os.Mkdir(child, 0o750); err != nil {
			t.Fatal(err)
		}
		if code, out := runInitAt(t, child); code != ExitOK {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
}
