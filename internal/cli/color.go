package cli

import (
	"io"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// painter applies terminal-palette ANSI to list/show glyphs and muted
// metadata. Color is off when stdout is not a TTY, when NO_COLOR is set,
// or when ATASK_COLOR=never. ATASK_COLOR=always forces it (tests).
type painter struct {
	color bool
}

func colorEnabled(w io.Writer, getenv func(string) string) bool {
	if getenv == nil {
		return false
	}
	if getenv("NO_COLOR") != "" {
		return false
	}
	switch strings.ToLower(getenv("ATASK_COLOR")) {
	case "never", "0", "false":
		return false
	case "always", "1", "true":
		return true
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

func (c *ctxt) painter() painter {
	return painter{color: colorEnabled(c.env.Stdout, c.env.Getenv)}
}

const (
	ansiGreen   = "32"
	ansiBlue    = "34"
	ansiDefault = "39"
	ansiDim     = "2"
	ansiUndim   = "22"
)

func (p painter) wrap(s, on, off string) string {
	if !p.color || s == "" {
		return s
	}
	return "\x1b[" + on + "m" + s + "\x1b[" + off + "m"
}

func (p painter) glyph(st task.Status, started bool) string {
	g := task.Glyph(st, started)
	if !p.color {
		return g
	}
	switch g {
	case "●":
		return p.wrap("●", ansiGreen, ansiDefault)
	case "◐":
		return p.wrap("◐", ansiBlue, ansiDefault)
	}
	return g
}

func (p painter) glyphRaw(g string) string {
	if !p.color {
		return g
	}
	switch g {
	case "●":
		return p.wrap("●", ansiGreen, ansiDefault)
	case "◐":
		return p.wrap("◐", ansiBlue, ansiDefault)
	}
	return g
}

func (p painter) dim(s string) string {
	return p.wrap(s, ansiDim, ansiUndim)
}
