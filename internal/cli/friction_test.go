package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func TestRenderEvidenceLabelsRevisions(t *testing.T) {
	branch, merge := "aaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbb"
	var b bytes.Buffer
	renderEvidence(&b, []verification.Evidence{
		{CheckID: "test", Required: true, Outcome: verification.OutcomePassed, Revision: branch},
		{CheckID: "test", Required: true, Outcome: verification.OutcomePassed, Revision: merge},
	}, false)
	out := b.String()
	if !strings.Contains(out, "@aaaaaaaaaaaa") || !strings.Contains(out, "@bbbbbbbbbbbb") {
		t.Fatalf("rows not labeled with their revisions:\n%s", out)
	}
	// One revision: no labels.
	b.Reset()
	renderEvidence(&b, []verification.Evidence{{CheckID: "test", Required: true, Outcome: verification.OutcomePassed, Revision: branch}}, false)
	if strings.Contains(b.String(), "@") {
		t.Fatalf("single-revision run labeled:\n%s", b.String())
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
