//go:build windows

package proof

import "syscall"

// detachedProcAttr starts the process in its own process group.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
