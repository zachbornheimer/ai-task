package proof

import (
	"context"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// TestSizeOptionOnAnyCheck: size= is a reserved option on every check spec;
// it picks the budget, eternal has none, a bad value is INVALID_INPUT, and it
// overrides a preset's own size.
func TestSizeOptionOnAnyCheck(t *testing.T) {
	cases := []struct {
		name   string
		parse  func() (ProofSpec, error)
		size   Size
		budget time.Duration
	}{
		{"proof", func() (ProofSpec, error) { return ParseDefaulting("run: go vet ./... size=medium", SizeSmall) }, SizeMedium, 300 * time.Second},
		{"proof default", func() (ProofSpec, error) { return ParseDefaulting("run: go vet ./...", SizeSmall) }, SizeSmall, 30 * time.Second},
		{"regression", func() (ProofSpec, error) { return Parse("run: go test ./... size=large") }, SizeLarge, 900 * time.Second},
		{"eternal", func() (ProofSpec, error) { return Parse("run: tail -f x size=eternal") }, SizeEternal, 0},
		{"preset size overridden", func() (ProofSpec, error) { return Parse("playwright_run: spec size=small") }, SizeSmall, 30 * time.Second},
		{"on_complete", func() (ProofSpec, error) {
			a, err := ParseAction("run: ./deploy.sh size=eternal", false)
			return a.Proof(), err
		}, SizeEternal, 0},
		{"on_complete default", func() (ProofSpec, error) {
			a, err := ParseAction("run: ./deploy.sh", true)
			return a.Proof(), err
		}, SizeLarge, 900 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec, err := c.parse()
			if err != nil {
				t.Fatal(err)
			}
			if spec.Size != c.size || spec.Size.Budget() != c.budget {
				t.Fatalf("size = %q budget %s, want %q %s", spec.Size, spec.Size.Budget(), c.size, c.budget)
			}
			if _, leaked := spec.Options[SizeOption]; leaked {
				t.Fatalf("size must not stay in options: %v", spec.Options)
			}
		})
	}
	if _, err := Parse("run: go test ./... size=huge"); !fault.Is(err, fault.CodeInvalidInput) {
		t.Fatalf("bad size err = %v, want INVALID_INPUT", err)
	}
	res := NewRunner().Run(context.Background(), CheckRegression, ProofSpec{Preset: "run", Subject: "true", Size: SizeEternal}, t.TempDir())
	if !res.Passed || res.BudgetMS != nil {
		t.Fatalf("eternal run = passed %v budget_ms %v, want passed with nil budget", res.Passed, res.BudgetMS)
	}
}
