package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func TestCheckRejectsShellSyntax(t *testing.T) {
	for _, raw := range []string{
		`fmt: test -z "$(gofmt -l .)"`,
		"fmt: test -z `gofmt -l .`",
		`env: test -n "$HOME"`,
		`env: echo ${HOME}`,
	} {
		_, err := parseCheck(raw, 1, true)
		if err == nil || !strings.Contains(err.Error(), "sh -c") || !strings.Contains(err.Error(), "without a shell") {
			t.Errorf("%s: want the sh -c remedy, got %v", raw, err)
		}
	}
	// A shell's own script may hold any syntax; a bare $ is a regexp
	// anchor; $1 is an awk field.
	for _, raw := range []string{
		`fmt: sh -c 'test -z "$(gofmt -l .)"'`,
		`fmt: bash -c 'echo $HOME'`,
		`unit: go test ./... -run 'TestX$'`,
		`awk: awk '{print $1}' f.txt`,
	} {
		spec, err := parseCheck(raw, 1, true)
		if err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		// Checks are echoed with the quoting they really run with.
		if got := app.ShellJoin(spec.Command); !strings.Contains(raw, got) {
			t.Errorf("%s echoed as %q", raw, got)
		}
	}
}

func TestRenderEvidenceLabelsRevisions(t *testing.T) {
	branch, merge := "aaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbb"
	var b bytes.Buffer
	renderEvidence(&b, []verification.Evidence{
		{CheckID: "test", Required: true, Outcome: verification.OutcomePassed, Revision: branch},
		{CheckID: "test", Required: true, Outcome: verification.OutcomePassed, Revision: merge},
	}, false)
	out := b.String()
	if !strings.Contains(out, "@aaaaaaaaaaaa") || !strings.Contains(out, "@bbbbbbbbbbbb") {
		t.Fatalf("rows not labelled with their revisions:\n%s", out)
	}
	// One revision: no labels.
	b.Reset()
	renderEvidence(&b, []verification.Evidence{{CheckID: "test", Required: true, Outcome: verification.OutcomePassed, Revision: branch}}, false)
	if strings.Contains(b.String(), "@") {
		t.Fatalf("single-revision run labelled:\n%s", b.String())
	}
}

func TestHelpNamesDefaults(t *testing.T) {
	_, out, _ := runAt(t, "help")
	if !strings.Contains(out, "--attempts") {
		t.Fatalf("at help does not name --attempts:\n%s", out)
	}
	// With ATASK_DB unset, --db's help names where the database lives.
	var o, e bytes.Buffer
	Main(Env{Args: []string{"status", "--help"}, Stdout: &o, Stderr: &e, Cwd: t.TempDir(), Getenv: func(string) string { return "" }})
	def, err := app.DefaultPath()
	if err != nil {
		t.Skip(err)
	}
	if !strings.Contains(o.String(), "default "+def) {
		t.Fatalf("--db help lacks the default %s:\n%s", def, o.String())
	}
}
