package proof

import (
	"reflect"
	"testing"
)

func TestPresetLangTests(t *testing.T) {
	cases := []struct {
		name          string
		bare, named   []string
		zero, passing string
	}{
		{"pl_test", []string{"prove", "-v", "t/a.t"}, nil, "Files=0, Tests=0", "Files=1, Tests=3\nResult: PASS"},
		{"php_test", []string{"phpunit", "tests"}, []string{"phpunit", "tests", "--filter", "Foo"}, "No tests executed!", "OK (3 tests, 3 assertions)"},
		{"rb_test", []string{"rspec", "spec"}, []string{"rspec", "spec", "-e", "Foo"}, "0 examples, 0 failures", "3 examples, 0 failures"},
		{"swift_test", []string{"swift", "test"}, []string{"swift", "test", "--filter", "Foo"}, "Executed 0 tests, with 0 failures", "Executed 3 tests, with 0 failures"},
		{"sh_test", []string{"bats", "t.bats"}, []string{"bats", "t.bats", "-f", "Foo"}, "0 tests, 0 failures", "1..3\nok 1 a"},
	}
	for _, c := range cases {
		p, ok := DefaultRegistry().Lookup(c.name)
		if !ok {
			t.Fatalf("%s not registered", c.name)
		}
		subj := map[string]string{"pl_test": "t/a.t", "php_test": "tests", "rb_test": "spec", "swift_test": "", "sh_test": "t.bats"}[c.name]
		got, err := p.Argv(ProofSpec{Preset: c.name, Subject: subj, Options: map[string]string{}}, Env{Workdir: t.TempDir()})
		if err != nil || !reflect.DeepEqual(got, c.bare) {
			t.Errorf("%s bare argv = %v, %v; want %v", c.name, got, err, c.bare)
		}
		if c.named != nil {
			got, _ = p.Argv(ProofSpec{Preset: c.name, Subject: subj, Options: map[string]string{"name": "Foo"}}, Env{Workdir: t.TempDir()})
			if !reflect.DeepEqual(got, c.named) {
				t.Errorf("%s named argv = %v; want %v", c.name, got, c.named)
			}
		}
		j, ok := p.(Judger)
		if !ok {
			t.Fatalf("%s is not a Judger", c.name)
		}
		if pass, _ := j.Judge(ProofSpec{}, []byte(c.zero), 0); pass {
			t.Errorf("%s passed a zero-tests run", c.name)
		}
		if pass, why := j.Judge(ProofSpec{}, []byte(c.passing), 0); !pass {
			t.Errorf("%s failed a passing run: %s", c.name, why)
		}
		if pass, _ := j.Judge(ProofSpec{}, []byte(c.passing), 1); pass {
			t.Errorf("%s passed a nonzero exit", c.name)
		}
	}
}
