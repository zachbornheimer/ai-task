package discard

import (
	"errors"
	"testing"
)

type closeSpy struct {
	closed bool
	err    error
}

func (c *closeSpy) Close() error { c.closed = true; return c.err }

func TestCloseReadOnlyClosesAndSwallowsError(t *testing.T) {
	spy := &closeSpy{err: errors.New("boom")}
	CloseReadOnly(spy)
	if !spy.closed {
		t.Fatal("handle was not closed")
	}
}
