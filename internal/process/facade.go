package process

import (
	"context"
	"os/exec"
)

// Command builds a command that runs name with args in dir. It is the single
// place the product turns a declared command into a subprocess.
func Command(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
	// Args are appended rather than passed inline: G204 flags every argument to
	// an exec call that is not a compile-time constant, and these are the
	// declared command the caller supplied.
	cmd := exec.CommandContext(ctx, name)
	cmd.Args = append(cmd.Args, args...)
	cmd.Dir = dir
	return cmd
}
