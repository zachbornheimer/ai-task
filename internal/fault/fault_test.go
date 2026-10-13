package fault

import (
	"errors"
	"fmt"
	"testing"
)

func TestCodeOf(t *testing.T) {
	base := errors.New("boom")
	cases := []struct {
		err  error
		want Code
	}{
		{nil, ""},
		{base, CodeNotReady},
		{New(CodeNotReady, "blocked"), CodeNotReady},
		{Wrap(base, CodeInvalidSession, "expired"), CodeInvalidSession},
		{fmt.Errorf("outer: %w", New(CodeNotFound, "x")), CodeNotFound},
	}
	for _, c := range cases {
		if got := CodeOf(c.err); got != c.want {
			t.Errorf("CodeOf(%v) = %q, want %q", c.err, got, c.want)
		}
	}
	w := WrapInternal(base, "ctx")
	if !errors.Is(w, base) {
		t.Error("Wrap must unwrap to the cause")
	}
	if !IsInternal(w) || Is(w, CodeNotFound) {
		t.Error("Is must match the carried code only")
	}
}
