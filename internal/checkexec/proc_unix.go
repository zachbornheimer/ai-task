//go:build !windows

package checkexec

import (
	"os/exec"
	"syscall"
)

// isolate starts cmd in its own process group and makes cancellation kill
// the whole group, so children a check leaves running in the background die
// with it and a timeout does not wait on pipes they hold open.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
