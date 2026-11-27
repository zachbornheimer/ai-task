//go:build !windows

package cli

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func privateRuntimePath(info fs.FileInfo, directory bool) error {
	mode := fs.FileMode(0600)
	if directory {
		mode = 0700
	}
	if info.Mode()&(fs.ModeSymlink|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) || info.Mode().Perm() != mode {
		return fmt.Errorf("expected a non-symlink private %04o path", mode)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("path is not owned by the current user")
	}
	return nil
}

func openRuntimeRegistration(root *os.Root, name string) (*os.File, error) {
	// Nonblocking also prevents a replacement FIFO from hanging the CLI before
	// the descriptor's identity and regular-file mode can be checked.
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

func runtimeParent(ctx context.Context, pid int) (int, error) {
	if runtime.GOOS == "linux" {
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return 0, fmt.Errorf("inspect process %d: %w", pid, err)
		}
		return runtimeProcParent(string(raw), pid)
	}
	cmd := exec.CommandContext(ctx, "ps", "-o", "ppid=,stat=", "-p", strconv.Itoa(pid))
	cmd.WaitDelay = runtimeProcessTimeout
	raw, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("inspect process %d with ps: %w", pid, err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 || strings.ContainsAny(fields[1], "ZX") {
		return 0, fmt.Errorf("process %d is not live or has invalid ps metadata", pid)
	}
	return runtimeParentNumber(fields[0], pid)
}

func runtimeProcParent(stat string, pid int) (int, error) {
	// comm can contain spaces and closing parentheses. The last closing
	// parenthesis delimits it; state and PPid are the next two fields.
	end := strings.LastIndex(stat, ") ")
	if end < 0 {
		return 0, fmt.Errorf("process %d has invalid proc metadata", pid)
	}
	fields := strings.Fields(stat[end+2:])
	if len(fields) < 2 || fields[0] == "Z" || fields[0] == "X" {
		return 0, fmt.Errorf("process %d is not live or has invalid proc metadata", pid)
	}
	return runtimeParentNumber(fields[1], pid)
}

func runtimeParentNumber(value string, pid int) (int, error) {
	parent, err := strconv.Atoi(value)
	if err != nil || parent < 0 {
		return 0, fmt.Errorf("process %d has invalid parent PID", pid)
	}
	return parent, nil
}
