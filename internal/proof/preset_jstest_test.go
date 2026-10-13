package proof

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPresetJSTest(t *testing.T) {
	p := jsTestPreset{}
	argvFor := func(lock string, opts map[string]string) []string {
		dir := t.TempDir()
		if lock != "" {
			if err := os.WriteFile(filepath.Join(dir, lock), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		got, err := p.Argv(ProofSpec{Preset: "js_test", Subject: "src/a.test.ts", Options: opts}, Env{Workdir: dir})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	cases := []struct {
		lock string
		opts map[string]string
		want []string
	}{
		{"pnpm-lock.yaml", nil, []string{"pnpm", "test", "--", "src/a.test.ts"}},
		{"yarn.lock", nil, []string{"yarn", "test", "src/a.test.ts"}},
		{"bun.lockb", nil, []string{"bun", "test", "src/a.test.ts"}},
		{"bun.lock", nil, []string{"bun", "test", "src/a.test.ts"}},
		{"", nil, []string{"npm", "test", "--", "src/a.test.ts"}},
		{"", map[string]string{"name": "adds"}, []string{"npm", "test", "--", "src/a.test.ts", "-t", "adds"}},
	}
	for _, c := range cases {
		if got := argvFor(c.lock, c.opts); !reflect.DeepEqual(got, c.want) {
			t.Errorf("lock %q opts %v: argv %v, want %v", c.lock, c.opts, got, c.want)
		}
	}
	if _, err := p.Argv(ProofSpec{}, Env{Workdir: t.TempDir()}); err == nil {
		t.Error("empty path accepted")
	}
	if _, ok := NewRegistry().Lookup("js_test"); ok {
		t.Error("NewRegistry should start empty")
	}
	if _, ok := DefaultRegistry().Lookup("js_test"); !ok {
		t.Error("js_test not in default registry")
	}

	judge := []struct {
		name, out string
		code      int
		want      bool
	}{
		{"vitest zero", "No test files found, exiting with code 0\nNo tests found", 0, false},
		{"jest zero", "Tests:       0 total\n", 0, false},
		{"zero passed", "0 passed", 0, false},
		{"normal", "Tests:       3 passed, 3 total\n", 0, true},
		{"failed", "Tests: 1 failed, 2 passed", 1, false},
	}
	for _, c := range judge {
		if ok, _ := p.Judge(ProofSpec{}, []byte(c.out), c.code); ok != c.want {
			t.Errorf("%s: Judge = %v, want %v", c.name, ok, c.want)
		}
	}
}
