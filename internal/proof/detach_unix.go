//go:build !windows

package proof

import "syscall"

// detachedProcAttr starts the process in a new session, out of the caller's
// process group and terminal.
func detachedProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
