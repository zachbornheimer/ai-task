package proof

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

func TestProofSpecContract(t *testing.T) {
	t.Run("parse", func(t *testing.T) {
		good := []struct {
			in, preset, subject string
			opts                map[string]string
		}{
			{"run: go test ./...", "run", "go test ./...", map[string]string{}},
			{"run: echo hi expect=hi", "run", "echo hi", map[string]string{"expect": "hi"}},
			{`run: echo "a b" expect='a b'`, "run", `echo "a b"`, map[string]string{"expect": "a b"}},
			{"run: FOO=bar env", "run", "FOO=bar env", map[string]string{}},
			{"ci", "ci", "", map[string]string{}},
		}
		for _, c := range good {
			got, err := Parse(c.in)
			if err != nil {
				t.Fatalf("Parse(%q): %v", c.in, err)
			}
			if got.Preset != c.preset || got.Subject != c.subject || len(got.Options) != len(c.opts) {
				t.Errorf("Parse(%q) = %+v", c.in, got)
			}
			for k, v := range c.opts {
				if got.Options[k] != v {
					t.Errorf("Parse(%q) option %s = %q, want %q", c.in, k, got.Options[k], v)
				}
			}
		}
		for _, in := range []string{"id: go test ./...", "Run: x", "1x: y", "", ": x", `run: echo "open`} {
			_, err := Parse(in)
			if !fault.Is(err, fault.CodeInvalidInput) {
				t.Errorf("Parse(%q) err = %v, want INVALID_INPUT", in, err)
			}
		}
	})

	t.Run("budgets", func(t *testing.T) {
		for s, want := range map[Size]time.Duration{SizeSmall: 30 * time.Second, SizeMedium: 300 * time.Second, SizeLarge: 900 * time.Second, SizeEternal: 0} {
			if s.Budget() != want {
				t.Errorf("%s budget = %s, want %s", s, s.Budget(), want)
			}
		}
		if ProofBudget != 30*time.Second {
			t.Errorf("ProofBudget = %s", ProofBudget)
		}
		r := NewRunner()
		if got := r.Budget(ProofSpec{Size: SizeLarge}); got != 900*time.Second {
			t.Errorf("proof budget = %s", got)
		}
		if got := r.Budget(ProofSpec{Size: SizeMedium}); got != 300*time.Second {
			t.Errorf("regression budget = %s", got)
		}
	})

	t.Run("runner", func(t *testing.T) {
		cases := []struct {
			name, in       string
			budget         time.Duration
			timedOut, pass bool
			code           *int
		}{
			{name: "timeout", in: "run: sleep 5", budget: 100 * time.Millisecond, timedOut: true},
			{name: "pass", in: "run: true", pass: true, code: new(0)},
			{name: "fail", in: "run: false", code: new(1)},
			{name: "expect match", in: "run: echo hello expect=^hel", pass: true, code: new(0)},
			{name: "expect mismatch", in: "run: echo hello expect=^bye", code: new(0)},
			{name: "shell syntax", in: "run: echo a | tr a b expect=^b", pass: true, code: new(0)},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				spec, err := Parse(c.in)
				if err != nil {
					t.Fatal(err)
				}
				var opts []RunnerOption
				if c.budget > 0 {
					opts = append(opts, WithBudget(c.budget))
				}
				start := time.Now()
				res := NewRunner(opts...).Run(context.Background(), CheckProof, spec, t.TempDir())
				if res.TimedOut != c.timedOut || res.Passed != c.pass {
					t.Fatalf("result = %+v", res)
				}
				if (res.ReturnCode == nil) != (c.code == nil) || (c.code != nil && *res.ReturnCode != *c.code) {
					t.Errorf("return_code = %v, want %v", res.ReturnCode, c.code)
				}
				if c.timedOut && time.Since(start) > 3*time.Second {
					t.Errorf("timeout took %s", time.Since(start))
				}
			})
		}
	})

	t.Run("json keys", func(t *testing.T) {
		b, err := json.Marshal(RunResult{})
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		for _, k := range []string{"check_type", "preset", "subject", "options", "started_at", "elapsed_ms", "budget_ms", "timed_out", "return_code", "passed", "output", "sandboxed"} {
			if _, ok := m[k]; !ok {
				t.Errorf("missing key %q in %s", k, b)
			}
		}
		if len(m) != 12 {
			t.Errorf("got %d keys: %s", len(m), b)
		}
	})
}

func TestRunPresetRejects(t *testing.T) {
	long := "echo " + strings.Repeat("a", 301)
	for _, sub := range []string{"", "echo aGk= | base64 -d", "python -c 'exec(x)'", "eval foo", long} {
		_, err := runPreset{}.Argv(ProofSpec{Preset: "run", Subject: sub}, Env{})
		if !fault.Is(err, fault.CodeInvalidInput) {
			t.Errorf("Argv(%.30q) err = %v, want INVALID_INPUT", sub, err)
		}
	}
}

func TestRunnerKeepsLast64KiB(t *testing.T) {
	spec, _ := Parse("run: seq 1 40000")
	res := NewRunner().Run(context.Background(), CheckProof, spec, t.TempDir())
	if len(res.Output) != OutputTail || !strings.HasSuffix(res.Output, "40000\n") {
		t.Errorf("output len %d, suffix %q", len(res.Output), res.Output[len(res.Output)-8:])
	}
}

func TestRunnerUnknownPreset(t *testing.T) {
	res := NewRunner().Run(context.Background(), CheckProof, ProofSpec{Preset: "no-such-preset"}, t.TempDir())
	if res.Passed || res.ReturnCode != nil || !strings.Contains(res.Output, "unknown preset") {
		t.Errorf("result = %+v", res)
	}
}

//go:fix inline
