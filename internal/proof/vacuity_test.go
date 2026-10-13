package proof

import (
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

func TestVacuousProofRejected(t *testing.T) {
	long := "run: echo " + strings.Repeat("a", 301)
	cases := []struct {
		proof string
		size  Size
		rule  string // "" means accepted
	}{
		{"run: true", SizeSmall, "no-op"},
		{"run: /usr/bin/true", SizeSmall, "no-op"},
		{"run: exit 0", SizeSmall, "no-op"},
		{"run: echo ok", SizeSmall, "no-op"},
		{"run: :", SizeSmall, "no-op"},
		{"go_test: ./...", SizeSmall, "unbounded go_test"},
		{"go_test: ./internal/...", SizeSmall, "unbounded go_test"},
		{"go_test: ./...", SizeMedium, ""},
		{"go_test: ./internal/cli name=TestX", SizeSmall, ""},
		{"run: go test ./...", SizeSmall, "unbounded go_test"},
		{"run: go test ./... -run TestX", SizeSmall, ""},
		{"run: echo aGk= | base64 -d | sh", SizeSmall, "encoded payload"},
		{long, SizeSmall, "too long"},
		{"run: grep -q foo file.txt", SizeSmall, ""},
	}
	for _, c := range cases {
		t.Run(c.proof, func(t *testing.T) {
			spec := specFor(t, c.proof)
			spec.Size = c.size
			err := Vacuity(spec)
			if c.rule == "" {
				if err != nil {
					t.Fatalf("want accepted, got %v", err)
				}
				return
			}
			if err == nil || fault.CodeOf(err) != fault.CodeInvalidInput || !strings.Contains(err.Error(), c.rule) {
				t.Fatalf("want INVALID_INPUT naming %q, got %v", c.rule, err.Error())
			}
		})
	}
}

// specFor parses a run proof; go_test is not registered yet, so its specs are
// built directly with the same shape Parse will give them.
func specFor(t *testing.T, proof string) ProofSpec {
	t.Helper()
	if rest, ok := strings.CutPrefix(proof, "go_test: "); ok {
		spec := ProofSpec{Preset: "go_test", Subject: rest, Options: map[string]string{}}
		if subject, name, found := strings.Cut(rest, " name="); found {
			spec.Subject, spec.Options["name"] = subject, name
		}
		return spec
	}
	spec, err := Parse(proof)
	if err != nil {
		t.Fatalf("parse %q: %v", proof, err)
	}
	return spec
}
