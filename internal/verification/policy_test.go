package verification

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

func TestPolicyDigestIsStableAndContentSensitive(t *testing.T) {
	a := Policy{TaskChecks: []CheckSpec{{ID: "unit", Command: []string{"go", "test", "./..."}, Timeout: 5 * time.Minute, Required: true}}}
	b := Policy{TaskChecks: []CheckSpec{{ID: "unit", Command: []string{"go", "test", "./..."}, Timeout: 5 * time.Minute, Required: true}}}
	if a.Digest() != b.Digest() {
		t.Fatal("equal policies must share a digest")
	}
	c := b
	c.TaskChecks = []CheckSpec{{ID: "unit", Command: []string{"go", "test", "./..."}, Timeout: 6 * time.Minute, Required: true}}
	if a.Digest() == c.Digest() {
		t.Fatal("timeout change must change digest")
	}
	d := b
	d.TaskChecks = []CheckSpec{{ID: "unit", Command: []string{"go", "test", "./..."}, Timeout: 5 * time.Minute, Required: false}}
	if a.Digest() == d.Digest() {
		t.Fatal("required flag change must change digest")
	}
	if !strings.HasPrefix(a.Digest(), "sha256:") {
		t.Fatalf("unexpected digest form %q", a.Digest())
	}
}

func TestPolicyRoundTripUsesMilliseconds(t *testing.T) {
	p := Policy{Regression: []CheckSpec{{ID: "r", Version: "2", Command: []string{"make", "test"}, Dir: "sub", Timeout: 1500 * time.Millisecond, Required: true}}}
	b, err := p.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"timeout_ms":1500`) {
		t.Fatalf("durable form must use timeout_ms: %s", b)
	}
	back, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if back.Digest() != p.Digest() {
		t.Fatal("round trip changed digest")
	}
	if back.Regression[0].Timeout != 1500*time.Millisecond || back.Regression[0].Dir != "sub" {
		t.Fatalf("round trip lost fields: %+v", back.Regression[0])
	}
	// Empty policy parses from empty bytes.
	e, err := Parse(nil)
	if err != nil || !e.Empty() {
		t.Fatalf("empty parse: %v %+v", err, e)
	}
}

func TestPolicyValidate(t *testing.T) {
	cases := []struct {
		name string
		p    Policy
		code fault.Code
	}{
		{"ok", Policy{TaskChecks: []CheckSpec{{ID: "a", Command: []string{"true"}}}}, ""},
		{"empty ok", Policy{}, ""},
		{"bad id", Policy{TaskChecks: []CheckSpec{{ID: "a b", Command: []string{"true"}}}}, fault.CodeInvalidInput},
		{"empty id", Policy{TaskChecks: []CheckSpec{{Command: []string{"true"}}}}, fault.CodeInvalidInput},
		{"dup across groups", Policy{TaskChecks: []CheckSpec{{ID: "a", Command: []string{"true"}}}, Regression: []CheckSpec{{ID: "a", Command: []string{"true"}}}}, fault.CodeInvalidInput},
		{"no command", Policy{TaskChecks: []CheckSpec{{ID: "a"}}}, fault.CodeInvalidInput},
		{"negative timeout", Policy{TaskChecks: []CheckSpec{{ID: "a", Command: []string{"true"}, Timeout: -1}}}, fault.CodeInvalidInput},
		{"huge timeout", Policy{TaskChecks: []CheckSpec{{ID: "a", Command: []string{"true"}, Timeout: 48 * time.Hour}}}, fault.CodeInvalidInput},
	}
	for _, c := range cases {
		err := c.p.Validate()
		if fault.CodeOf(err) != c.code {
			t.Errorf("%s: got %v, want code %q", c.name, err, c.code)
		}
	}
}

func TestMergeTaskDefinitionsWin(t *testing.T) {
	task := Policy{TaskChecks: []CheckSpec{{ID: "unit", Command: []string{"a"}, Required: true}}, Regression: []CheckSpec{{ID: "lint", Command: []string{"task-lint"}, Required: true}}}
	proj := []CheckSpec{{ID: "lint", Command: []string{"proj-lint"}, Required: true}, {ID: "full", Command: []string{"proj-full"}, Required: true}}
	m := Merge(task, proj)
	if len(m.Regression) != 2 || m.Regression[0].Command[0] != "task-lint" || m.Regression[1].ID != "full" {
		t.Fatalf("unexpected merge: %+v", m.Regression)
	}
	if got := len(m.RequiredChecks()); got != 3 {
		t.Fatalf("required = %d", got)
	}
	// Merge must not alias the input slices.
	m.TaskChecks[0].ID = "changed"
	if task.TaskChecks[0].ID != "unit" {
		t.Fatal("merge aliased input")
	}
	var generic map[string]any
	b, _ := m.Canonical()
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatal(err)
	}
}

func TestEffectiveTimeout(t *testing.T) {
	if (CheckSpec{}).EffectiveTimeout() != DefaultCheckTimeout {
		t.Fatal("zero timeout must default")
	}
	if (CheckSpec{Timeout: time.Second}).EffectiveTimeout() != time.Second {
		t.Fatal("explicit timeout must be kept")
	}
}
