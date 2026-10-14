// Package discard holds the few places where an error is dropped on purpose.
package discard

import "io"

// CloseReadOnly closes a handle that was only read from. A failed close
// there loses no data and the caller cannot act on it, so the error is
// dropped deliberately. Never use it on a handle that was written to.
func CloseReadOnly(c io.Closer) {
	_ = c.Close()
}
