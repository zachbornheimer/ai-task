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

func TestMergeKeepsNamespacesSeparate(t *testing.T) {
	task := Policy{TaskChecks: []CheckSpec{{ID: "unit", Command: []string{"a"}, Required: true}}}
	proj := []CheckSpec{{ID: "lint", Command: []string{"proj-lint"}, Required: true}, {ID: "full", Command: []string{"proj-full"}, Required: true}}
	m := Merge(task, proj)
	if len(m.Regression) != 2 || m.Regression[0].Command[0] != "proj-lint" || len(m.TaskChecks) != 1 {
		t.Fatalf("unexpected merge: %+v", m)
	}
	if got := len(m.RequiredChecks()); got != 3 {
		t.Fatalf("required = %d", got)
	}
	if Collides([]CheckSpec{{ID: "lint"}}, proj) != "lint" || Collides([]CheckSpec{{ID: "unit"}}, proj) != "" {
		t.Fatal("collision detection")
	}
	if m.MissingCategory() != "" || (Policy{TaskChecks: task.TaskChecks}).MissingCategory() != "required project regression checks" || (Policy{Regression: proj}).MissingCategory() != "required task checks" {
		t.Fatal("missing category")
	}
	if _, err := ParseMode(""); err == nil {
		t.Fatal("bare verify must be invalid")
	}
	if len(m.Subset(ModeTask).Regression) != 0 || len(m.Subset(ModeRegression).TaskChecks) != 0 {
		t.Fatal("subset")
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

func TestRequireGateAndJSONRequiredDefault(t *testing.T) {
	if err := RequireGate(nil, "task checks"); err == nil {
		t.Fatal("empty list accepted as a gate")
	}
	if err := RequireGate([]CheckSpec{{ID: "o", Command: []string{"x"}}}, "task checks"); err == nil {
		t.Fatal("optional-only list accepted as a gate")
	}
	if err := RequireGate([]CheckSpec{{ID: "o", Command: []string{"x"}}, {ID: "r", Command: []string{"x"}, Required: true}}, "task checks"); err != nil {
		t.Fatal(err)
	}
	// Hand-written JSON that omits "required" describes a gate, not an
	// informational check; stored JSON always says so explicitly.
	var absent, explicitFalse CheckSpec
	if err := json.Unmarshal([]byte(`{"id":"x","command":["true"]}`), &absent); err != nil || !absent.Required {
		t.Fatalf("absent required should default to true: %+v %v", absent, err)
	}
	if err := json.Unmarshal([]byte(`{"id":"x","command":["true"],"required":false}`), &explicitFalse); err != nil || explicitFalse.Required {
		t.Fatalf("explicit false lost: %+v %v", explicitFalse, err)
	}
	b, _ := json.Marshal(CheckSpec{ID: "x", Command: []string{"true"}})
	if !strings.Contains(string(b), `"required":false`) {
		t.Fatalf("stored form must be explicit: %s", b)
	}
	if (Policy{TaskChecks: []CheckSpec{{ID: "o", Command: []string{"x"}}}, Regression: []CheckSpec{{ID: "r", Command: []string{"x"}, Required: true}}}).MissingCategory() == "" {
		t.Fatal("optional-only task checks counted as a category")
	}
}
