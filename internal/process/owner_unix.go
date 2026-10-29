//go:build !windows

package process

import (
	"errors"
	"syscall"
)

// pidExists probes pid with signal 0: ESRCH means no such process, and
// EPERM means one exists that this user may not signal.
func pidExists(pid int) (exists, probed bool) {
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, true
	case errors.Is(err, syscall.ESRCH):
		return false, true
	}
	return false, false
}
