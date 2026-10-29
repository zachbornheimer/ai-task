//go:build linux

package process

import (
	"os"
	"strconv"
	"strings"
)

// startTime is the process's start in clock ticks since boot, field 22 of
// /proc/<pid>/stat. Only equality matters, so the unit is left alone.
func startTime(pid int) (string, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	// The command name (field 2) may contain spaces and parentheses; the
	// fields after the last ')' start at field 3.
	i := strings.LastIndex(string(b), ") ")
	if i < 0 {
		return "", false
	}
	fields := strings.Fields(string(b)[i+2:])
	if len(fields) < 20 {
		return "", false
	}
	return fields[19], true
}
