// Package discard holds the few places where an error is dropped on purpose.
package discard

import (
	"fmt"
	"io"
)

// CloseReadOnly closes a handle whose close cannot lose data: it was only
// read from, or anything written to it is informational. The caller cannot
// act on a failed close, so the error is dropped deliberately. Never use it
// on a handle whose written content must reach disk.
func CloseReadOnly(c io.Closer) {
	_ = c.Close()
}

// Writef writes best-effort text that nothing depends on, such as an
// advisory log line or a stderr diagnostic. The error is dropped on purpose.
func Writef(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}

// Writeln is Writef for a whole line.
func Writeln(w io.Writer, a ...any) {
	_, _ = fmt.Fprintln(w, a...)
}
