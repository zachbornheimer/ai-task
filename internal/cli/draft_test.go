package cli

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var taskIDText = regexp.MustCompile(`at-[a-z0-9]+`)

func TestDraftWithoutProofs(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	repo := t.TempDir()
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := Main(Env{Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Cwd: repo, Getenv: func(k string) string {
			if k == "ATASK_DB" {
				return db
			}
			return ""
		}})
		return code, out.String(), errb.String()
	}
	if code, _, e := run("init", "--name", "drafts"); code != ExitOK {
		t.Fatalf("init: %d %s", code, e)
	}
	code, out, e := run("--json", "add", "idea")
	if code != ExitOK || !strings.Contains(out, `"status":"draft"`) {
		t.Fatalf("add without proofs must create a draft: exit %d\n%s\n%s", code, out, e)
	}
	id := taskIDText.FindString(out)

	_, out, _ = run("list")
	if !strings.Contains(out, "◌") || !strings.Contains(out, "idea") {
		t.Fatalf("list must show the draft glyph:\n%s", out)
	}
	if _, out, _ = run("list", "draft"); !strings.Contains(out, "idea") {
		t.Fatalf("list draft must return it:\n%s", out)
	}
	if _, out, _ = run("list", "ready"); strings.Contains(out, "idea") {
		t.Fatalf("list ready must not return a draft:\n%s", out)
	}
	code, out, e = run("claim", id)
	if code == ExitOK || !strings.Contains(out+e, "NOT_READY") || !strings.Contains(out+e, "--proof") || !strings.Contains(out+e, "--regression") {
		t.Fatalf("claim of a draft must be NOT_READY naming both flags: exit %d\n%s\n%s", code, out, e)
	}
	if _, out, _ = run("show", id); strings.Contains(out, "RETRIES") || strings.Contains(out, "ATTEMPT") {
		t.Fatalf("a draft shows no attempt budget:\n%s", out)
	}
	if _, out, _ = run("status"); !strings.Contains(out, "draft") {
		t.Fatalf("status must count drafts separately:\n%s", out)
	}

	if code, _, e = run("update", id, "--proof", "run: go version"); code != ExitOK {
		t.Fatalf("update one proof: %d %s", code, e)
	}
	if _, out, _ = run("list", "draft"); !strings.Contains(out, "idea") {
		t.Fatalf("one proof keeps it a draft:\n%s", out)
	}
	if code, _, e = run("update", id, "--proof", "run: go version", "--regression", "run: go version"); code != ExitOK {
		t.Fatalf("update both proofs: %d %s", code, e)
	}
	if _, out, _ = run("list", "ready"); !strings.Contains(out, "idea") {
		t.Fatalf("both proofs make it ready:\n%s", out)
	}
	if _, out, _ = run("list", "draft"); strings.Contains(out, "idea") {
		t.Fatalf("a ready task is no longer a draft:\n%s", out)
	}
}
