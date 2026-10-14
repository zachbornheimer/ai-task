package proof

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

const (
	streamAllGreen = `{"Action":"run","Test":"TestA"}
{"Action":"pass","Test":"TestA"}
{"Action":"pass","Package":"p"}
`
	streamOneFail = `{"Action":"pass","Test":"TestA"}
{"Action":"fail","Test":"TestB"}
{"Action":"fail","Package":"p"}
`
	streamNoTests = `{"Action":"output","Output":"testing: warning: no tests to run\n"}
{"Action":"pass","Package":"p"}
`
)

func TestPresetGoTest(t *testing.T) {
	p := goTestPreset{}
	spec := func(in string) ProofSpec {
		s, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		return s
	}

	t.Run("argv", func(t *testing.T) {
		got, err := p.Argv(spec("go_test: ./internal/proof name=^TestX$"), Env{})
		want := []string{"go", "test", "-count=1", "-json", "./internal/proof", "-run", "^TestX$"}
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("argv = %v, %v; want %v", got, err, want)
		}
		got, _ = p.Argv(spec("go_test: ./internal/proof"), Env{})
		if !slices.Equal(got, want[:5]) {
			t.Errorf("argv without name = %v", got)
		}
	})

	t.Run("judge", func(t *testing.T) {
		cases := []struct {
			name, out, test string
			code            int
			pass            bool
			reason          string
		}{
			{"all pass", streamAllGreen, "", 0, true, ""},
			{"one fail", streamOneFail, "", 1, false, "fail"},
			{"named test did not run", streamNoTests, "TestA", 0, false, "named test did not run"},
			{"name matches", streamAllGreen, "^TestA$", 0, true, ""},
			{"name matches only a failing test", streamOneFail, "TestB", 1, false, "fail"},
		}
		for _, c := range cases {
			s := ProofSpec{Preset: "go_test", Subject: "./x", Options: map[string]string{}}
			if c.test != "" {
				s.Options["name"] = c.test
			}
			pass, reason := p.Judge(s, []byte(c.out), c.code)
			if pass != c.pass || !strings.Contains(reason, c.reason) {
				t.Errorf("%s: got %v %q", c.name, pass, reason)
			}
		}
	})

	t.Run("whole tree needs a name", func(t *testing.T) {
		for _, in := range []string{"go_test: ./...", "go_test: ./internal/..."} {
			if _, err := Parse(in); !fault.Is(err, fault.CodeInvalidInput) {
				t.Errorf("Parse(%q) err = %v, want INVALID_INPUT", in, err)
			}
		}
		if _, err := Parse("go_test: ./... name=TestA"); err != nil {
			t.Errorf("named whole tree rejected: %v", err)
		}
	})

	t.Run("real run", func(t *testing.T) {
		dir := t.TempDir()
		files := map[string]string{
			"go.mod":    "module tmp\n\ngo 1.24\n",
			"x_test.go": "package tmp\n\nimport \"testing\"\n\nfunc TestGood(t *testing.T) {}\nfunc TestBad(t *testing.T) { t.Fatal(\"no\") }\n",
		}
		for n, c := range files {
			if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		r := NewRunner()
		run := func(in string) bool {
			return r.Run(context.Background(), CheckProof, spec(in), dir).Passed
		}
		if !run("go_test: . name=^TestGood$") {
			t.Error("passing named test failed")
		}
		if run("go_test: . name=^TestBad$") {
			t.Error("failing named test passed")
		}
		if run("go_test: . name=^NoSuchTest$") {
			t.Error("vacuous run passed")
		}
		if run("go_test: .") {
			t.Error("package with a failing test passed")
		}
	})
}
