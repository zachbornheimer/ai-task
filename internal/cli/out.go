package cli

import (
	"fmt"
	"io"
)

// errWriter latches the first write error so a renderer made of many
// small writes reports one failure at the end instead of checking each.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return len(p), nil
	}
	n, err := e.w.Write(p)
	e.err = err
	return n, err
}

// say formats to w. Errors are latched by the errWriter that emit wraps
// around stdout; for stderr diagnostics there is no better channel left.
func say(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}

// sayln writes a line to w; see say for how write errors surface.
func sayln(w io.Writer, a ...any) {
	_, _ = fmt.Fprintln(w, a...)
}
