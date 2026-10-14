package proof

import (
	"reflect"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/must"
)

func TestPresetPyTest(t *testing.T) {
	p, ok := DefaultRegistry().Lookup("py_test")
	if !ok {
		t.Fatal("py_test not registered")
	}
	if p.DefaultSize() != SizeSmall {
		t.Errorf("default size = %v, want small", p.DefaultSize())
	}
	argv, _ := p.Argv(ProofSpec{Preset: "py_test", Subject: "tests/test_a.py"}, Env{})
	if want := []string{"pytest", "tests/test_a.py", "-q"}; !reflect.DeepEqual(argv, want) {
		t.Errorf("argv = %v, want %v", argv, want)
	}
	argv, _ = p.Argv(ProofSpec{Preset: "py_test", Subject: "t.py", Options: map[string]string{"name": "foo and bar"}}, Env{})
	if want := []string{"pytest", "t.py", "-q", "-k", "foo and bar"}; !reflect.DeepEqual(argv, want) {
		t.Errorf("argv = %v, want %v", argv, want)
	}
	j := must.As[Judger](p)
	for _, c := range []struct {
		code   int
		pass   bool
		reason string
	}{{0, true, ""}, {5, false, "no tests collected"}, {1, false, "pytest exited 1"}} {
		pass, reason := j.Judge(ProofSpec{}, nil, c.code)
		if pass != c.pass || reason != c.reason {
			t.Errorf("Judge(%d) = %v, %q; want %v, %q", c.code, pass, reason, c.pass, c.reason)
		}
	}
}
