package process

import (
	"context"
	"os/exec"
)

// Command builds a command that runs name with args in dir. It is the single
// place the product turns a declared command into a subprocess.
func Command(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd
}
