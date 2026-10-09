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
		{base, CodeInternal},
		{New(CodeTaskBlocked, "blocked"), CodeTaskBlocked},
		{Wrap(base, CodeLeaseExpired, "expired"), CodeLeaseExpired},
		{fmt.Errorf("outer: %w", New(CodeNotFound, "x")), CodeNotFound},
	}
	for _, c := range cases {
		if got := CodeOf(c.err); got != c.want {
			t.Errorf("CodeOf(%v) = %q, want %q", c.err, got, c.want)
		}
	}
	w := Wrap(base, CodeInternal, "ctx")
	if !errors.Is(w, base) {
		t.Error("Wrap must unwrap to the cause")
	}
	if !Is(w, CodeInternal) || Is(w, CodeNotFound) {
		t.Error("Is must match the carried code only")
	}
}
