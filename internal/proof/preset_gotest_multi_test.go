package proof

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":      "module example.com/m\n\ngo 1.24\n",
		"a/a_test.go": "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n",
		"b/b_test.go": "package b\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) {}\n",
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPresetGoTestMultiPackage(t *testing.T) {
	spec := func(in string) ProofSpec {
		s, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		return s
	}

	t.Run("argv splits packages", func(t *testing.T) {
		got, err := goTestPreset{}.Argv(spec("go_test: ./internal/app ./internal/cli name=^TestX$"), Env{})
		want := []string{"go", "test", "-count=1", "-json", "./internal/app", "./internal/cli", "-run", "^TestX$"}
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("Argv = %v, %v; want %v", got, err, want)
		}
	})

	t.Run("runs two packages", func(t *testing.T) {
		dir := writeModule(t)
		res := NewRunner().Run(context.Background(), CheckRegression, spec("go_test: ./a ./b"), dir)
		if !res.Passed {
			t.Fatalf("want pass, got %+v", res)
		}
	})

	t.Run("bad package shows go error", func(t *testing.T) {
		dir := writeModule(t)
		res := NewRunner().Run(context.Background(), CheckRegression, spec("go_test: ./a ./nope"), dir)
		if res.Passed {
			t.Fatalf("want fail, got pass")
		}
		if strings.Contains(res.Output, "./a ./nope") || !strings.Contains(res.Output, "nope") {
			t.Fatalf("output should be go's own error for ./nope: %s", res.Output)
		}
	})
}
