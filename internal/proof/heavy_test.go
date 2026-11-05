package proof

import "testing"

func TestHeavyWithoutSize(t *testing.T) {
	cases := []struct {
		raw   string
		avoid bool // want no warning
	}{
		{"run: cargo test", false},
		{"run: cargo test -p core", false},
		{"run: cargo build --release", false},
		{"run: go test ./...", false},
		{"run: npm ci", false},
		{"run: pnpm install && pnpm test", false},
		{"run: docker build .", false},
		{"run: mise run ci", false},
		{"run: make test", false},
		// sized: the author already chose a budget
		{"run: cargo test size=medium", true},
		{"run: go test ./... size=large", true},
		// light commands
		{"run: test -f go.mod", true},
		{"run: go test ./internal/app -run TestX", true},
		{"run: grep -q ok out.txt", true},
		// presets that carry their own default size
		{"ci: build", true},
		{"build: all", true},
		{"rs_test: .", true},
		{"", true},
	}
	for _, c := range cases {
		got := HeavyWithoutSize(c.raw)
		if c.avoid && got != "" {
			t.Errorf("HeavyWithoutSize(%q) = %q, want no warning", c.raw, got)
		}
		if !c.avoid && got == "" {
			t.Errorf("HeavyWithoutSize(%q) = no warning, want one naming size=", c.raw)
		}
	}
}
