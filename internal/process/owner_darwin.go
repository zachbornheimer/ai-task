//go:build darwin

package process

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// startTime is the process's start as the kernel records it (seconds and
// microseconds since the epoch).
func startTime(pid int) (string, bool) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(k.Proc.P_pid) != pid {
		return "", false
	}
	t := k.Proc.P_starttime
	return strconv.FormatInt(int64(t.Sec), 10) + "." + strconv.FormatInt(int64(t.Usec), 10), true
}
