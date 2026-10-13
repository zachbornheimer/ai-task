package proof

import (
	"reflect"
	"testing"
)

func TestPresetRSTest(t *testing.T) {
	p, ok := DefaultRegistry().Lookup("rs_test")
	if !ok {
		t.Fatal("rs_test not registered")
	}
	if p.DefaultSize() != SizeMedium {
		t.Errorf("default size = %q, want medium", p.DefaultSize())
	}
	argvCases := []struct {
		name string
		spec ProofSpec
		want []string
	}{
		{"workspace", ProofSpec{}, []string{"cargo", "test", "--", "--nocapture"}},
		{"crate", ProofSpec{Subject: "core"}, []string{"cargo", "test", "-p", "core", "--", "--nocapture"}},
		{
			"crate+name",
			ProofSpec{Subject: "core", Options: map[string]string{"name": "parse_.*"}},
			[]string{"cargo", "test", "-p", "core", "parse_.*", "--", "--nocapture"},
		},
	}
	for _, c := range argvCases {
		got, err := p.Argv(c.spec, Env{})
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: argv = %v, %v; want %v", c.name, got, err, c.want)
		}
	}
	j, ok := p.(Judger)
	if !ok {
		t.Fatal("rs_test does not implement Judger")
	}
	judgeCases := []struct {
		name   string
		out    string
		code   int
		passed bool
		reason string
	}{
		{"passed", "running 3 tests\ntest result: ok. 3 passed; 0 failed", 0, true, ""},
		{"second target", "running 0 tests\ntest result: ok. 0 passed; 0 failed\nrunning 2 tests\ntest result: ok. 2 passed; 0 failed", 0, true, ""},
		{"none ran", "running 0 tests\ntest result: ok. 0 passed; 0 failed\nrunning 0 tests\n", 0, false, "no tests ran"},
		{"failure", "test result: FAILED. 1 passed; 1 failed", 101, false, "exit code 101"},
	}
	for _, c := range judgeCases {
		passed, reason := j.Judge(ProofSpec{}, []byte(c.out), c.code)
		if passed != c.passed || reason != c.reason {
			t.Errorf("%s: Judge = %v, %q; want %v, %q", c.name, passed, reason, c.passed, c.reason)
		}
	}
}
