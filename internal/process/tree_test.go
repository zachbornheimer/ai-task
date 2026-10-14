//go:build !windows

package process

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alive reports whether pid is a running process; a zombie awaiting its
// reaper is dead for this purpose.
func alive(pid int) bool {
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		s := string(b)
		if i := strings.LastIndex(s, ")"); i >= 0 && len(s) > i+2 {
			return s[i+2] != 'Z'
		}
	}
	return syscall.Kill(pid, 0) == nil
}

// TestTimeoutKillsChildProcesses: a check that backgrounds a child and then
// outlives its timeout is stopped as a whole: Run returns timed out within
// about a second of the deadline, and the background child is gone too.
func TestTimeoutKillsChildProcesses(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "child.pid")
	start := time.Now()
	r := Run(context.Background(), Spec{Argv: sh("sleep 30 & echo $! > " + pidfile + "; sleep 5"), Timeout: time.Second})
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout enforced after %s, want under 3s", elapsed)
	}
	if !r.TimedOut || r.Err == nil || !strings.Contains(r.Err.Error(), "timed out") {
		t.Fatalf("not reported as timed out: %+v", r)
	}
	b, err := os.ReadFile(filepath.Clean(pidfile))
	if err != nil {
		t.Fatalf("the check never recorded its child: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("pid file: %q", b)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	for deadline := time.Now().Add(2 * time.Second); alive(pid); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("background child %d outlived the timed-out check", pid)
		}
	}
}
