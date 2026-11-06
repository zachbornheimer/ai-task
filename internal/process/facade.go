package process

import (
	"context"
	"os/exec"
	"time"
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

// descendantGrace bounds how long a canceled command's Wait lingers on output
// pipes that a surviving descendant still holds open.
const descendantGrace = 5 * time.Second

// Contained is Command for a tool that starts descendants of its own (git
// running hooks, a check running servers): canceling ctx ends everything the
// command started, not just the command, and Wait returns within
// descendantGrace even if something escaped. A plain Command only kills its
// direct child, so a descendant that inherited its output pipe holds
// CombinedOutput open until that descendant exits on its own.
func Contained(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
	cmd := Command(ctx, dir, name, args...)
	isolate(cmd)
	cmd.WaitDelay = descendantGrace
	return cmd
}
