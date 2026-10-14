package must

import (
	"errors"
	"testing"
)

func TestAsReturnsMatchingType(t *testing.T) {
	if got := As[string]("x"); got != "x" {
		t.Fatalf("got %q", got)
	}
}

func TestAsPanicsOnMismatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	As[string](1)
}

func TestValuePassesThrough(t *testing.T) {
	if got := Value(t, 3, nil); got != 3 {
		t.Fatalf("got %d", got)
	}
}

type spy struct {
	testing.TB
	failed bool
}

func (s *spy) Helper()           {}
func (s *spy) Fatal(args ...any) { s.failed = true }

func TestOKFailsOnError(t *testing.T) {
	s := &spy{TB: t}
	OK(s, errors.New("x"))
	if !s.failed {
		t.Fatal("OK did not fail")
	}
}
