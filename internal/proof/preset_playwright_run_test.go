package proof

import (
	"reflect"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/must"
)

func TestPresetPlaywrightRun(t *testing.T) {
	p, ok := DefaultRegistry().Lookup("playwright_run")
	if !ok {
		t.Fatal("playwright_run not registered")
	}
	if p.DefaultSize() != SizeMedium {
		t.Errorf("default size = %s, want medium", p.DefaultSize())
	}
	spec, err := Parse("playwright_run: e2e/a.spec.ts grep=login project=chromium")
	if err != nil {
		t.Fatal(err)
	}
	argv, err := p.Argv(spec, Env{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"npx", "playwright", "test", "e2e/a.spec.ts", "--reporter=json", "--grep", "login", "--project", "chromium"}
	if !reflect.DeepEqual(argv, want) {
		t.Errorf("argv = %v, want %v", argv, want)
	}
	bare, _ := p.Argv(ProofSpec{Preset: "playwright_run", Subject: "x.spec.ts"}, Env{})
	if !reflect.DeepEqual(bare, []string{"npx", "playwright", "test", "x.spec.ts", "--reporter=json"}) {
		t.Errorf("bare argv = %v", bare)
	}
	if _, err := p.Argv(ProofSpec{Preset: "playwright_run"}, Env{}); err == nil {
		t.Error("empty subject accepted")
	}

	j := must.As[Judger](p)
	for _, c := range []struct {
		name, out string
		pass      bool
	}{
		{"pass", `{"stats":{"expected":3,"unexpected":0,"flaky":0,"skipped":0}}`, true},
		{"unexpected", `{"stats":{"expected":2,"unexpected":1,"flaky":0}}`, false},
		{"flaky", `{"stats":{"expected":2,"unexpected":0,"flaky":1}}`, false},
		{"none", `{"stats":{"expected":0,"unexpected":0,"flaky":0}}`, false},
		{"garbage", `not json`, false},
	} {
		if ok, why := j.Judge(ProofSpec{}, []byte(c.out), 0); ok != c.pass || (!ok && why == "") {
			t.Errorf("%s: Judge = %v %q", c.name, ok, why)
		}
	}
}
