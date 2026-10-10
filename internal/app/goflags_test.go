package app_test

import "testing"

// TestNestedCheckEnvKeepsOneCountFlag: when the engine itself runs inside
// an at check (GOFLAGS already carries -count=1), a check sees exactly one
// -count=1, and other flags the caller set are kept.
func TestNestedCheckEnvKeepsOneCountFlag(t *testing.T) {
	for _, outer := range []string{"-count=1", "-mod=mod -count=1", "-mod=mod"} {
		t.Run(outer, func(t *testing.T) {
			want := outer
			if outer == "-mod=mod" {
				want = "-mod=mod -count=1"
			}
			t.Setenv("GOFLAGS", outer)
			f := newGitFixture(t)
			a := f.addWith("a", `test "$GOFLAGS" = "`+want+`"`)
			f.complete(f.claim(string(a)))
		})
	}
}
