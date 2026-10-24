package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// TestUnknownProjectIsRejected pins the --project contract: a selector that
// matches no registered project by id or name is INVALID_INPUT naming the
// selector and the registered names, on every verb that takes --project. It
// never falls back to general, so a typo cannot file work onto the wrong
// board. general stays the default only when --project is absent.
func TestUnknownProjectIsRejected(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	cwd := t.TempDir()
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := Main(Env{Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Cwd: cwd, Getenv: func(k string) string {
			if k == "ATASK_DB" {
				return db
			}
			return ""
		}})
		return code, out.String(), errb.String()
	}

	if code, _, e := run("project", "add", "AI Tooling"); code != ExitOK {
		t.Fatalf("project add: %d %s", code, e)
	}
	if code, _, e := run("add", "loose work", "--proof", "run: test -d .", "--regression", "run: test -d ."); code != ExitOK {
		t.Fatalf("add to general: %d %s", code, e)
	}

	// Every verb that takes --project refuses an unknown name the same way:
	// non-zero, INVALID_INPUT, the bad name, and the registered names.
	for _, tc := range []struct {
		bad  string
		verb []string
	}{
		{"zysys:webmaster", []string{"--project", "zysys:webmaster", "list"}},
		{"zysys:webmaster", []string{"list", "--project", "zysys:webmaster"}},
		{"zysys:webmaster", []string{"--project", "zysys:webmaster", "add", "typo task", "--proof", "run: test -d .", "--regression", "run: test -d ."}},
		{"nosuchrepo", []string{"--project", "nosuchrepo", "doctor"}},
	} {
		code, out, e := run(tc.verb...)
		if code == ExitOK {
			t.Fatalf("%v must exit non-zero:\n%s%s", tc.verb, out, e)
		}
		for _, want := range []string{"INVALID_INPUT", tc.bad, "AI Tooling"} {
			if !strings.Contains(out+e, want) {
				t.Fatalf("%v must name %q:\n%s%s", tc.verb, want, out, e)
			}
		}
	}

	// JSON mode carries the same code in the envelope.
	code, out, _ := run("--json", "--project", "zysys:webmaster", "list")
	if code == ExitOK || !strings.Contains(out, `"code":"INVALID_INPUT"`) {
		t.Fatalf("json envelope must carry INVALID_INPUT: %d\n%s", code, out)
	}

	// The typo'd add filed nothing: general still holds only the loose task.
	code, out, e := run("--project", "general", "list")
	if code != ExitOK {
		t.Fatalf("list general: %d %s", code, e)
	}
	if strings.Contains(out, "typo task") {
		t.Fatalf("typo task must not be filed:\n%s", out)
	}

	// general remains reachable by name and the default when --project is absent.
	for _, verb := range [][]string{{"--project", "general", "list"}, {"list"}} {
		if code, out, e = run(verb...); code != ExitOK || !strings.Contains(out, "loose work") {
			t.Fatalf("%v: %d\n%s%s", verb, code, out, e)
		}
	}
}
