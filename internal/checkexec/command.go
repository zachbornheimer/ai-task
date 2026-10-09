// Package checkexec runs external verification commands. It knows nothing
// about tasks or policies: it takes an argv, a directory, and a timeout, and
// returns what happened with bounded, separated output.
package checkexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// OutputLimit bounds each captured stream. The head and tail are kept so
// that both the first error and the final summary survive truncation.
const OutputLimit = 32 * 1024

// Spec describes one command to run.
type Spec struct {
	Argv    []string
	Dir     string
	Timeout time.Duration
	// Env, when non-nil, replaces the inherited environment.
	Env []string
}

// Result records what happened. ExitCode is -1 when the process did not
// exit normally (killed, not started).
type Result struct {
	StartedAt  time.Time
	FinishedAt time.Time
	ExitCode   int
	TimedOut   bool
	// Started is false when the executable could not be launched at all
	// (missing binary, bad directory); Err then explains why.
	Started bool
	Err     error
	Stdout  string
	Stderr  string
}

// Run executes the spec. It never uses a shell. The process is killed when
// the timeout elapses or ctx is cancelled; Go's WaitDelay guarantees Run
// returns even if the process ignores the signal and its children hold the
// pipes open.
func Run(ctx context.Context, spec Spec) Result {
	res := Result{StartedAt: time.Now().UTC(), ExitCode: -1}
	if len(spec.Argv) == 0 {
		res.Err = errors.New("empty command")
		res.FinishedAt = time.Now().UTC()
		return res
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = spec.Dir
	if spec.Env != nil {
		cmd.Env = spec.Env
	}
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr boundedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	res.FinishedAt = time.Now().UTC()
	res.Stdout, res.Stderr = stdout.String(), stderr.String()
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		res.Started, res.ExitCode = true, 0
	case errors.As(err, &exitErr):
		res.Started = true
		res.ExitCode = exitErr.ExitCode()
		if res.TimedOut {
			res.Err = fmt.Errorf("timed out after %s", timeout)
		} else if res.ExitCode == -1 {
			res.Err = err // killed by a signal
		}
	default:
		// Could not start: not found, permission, bad dir.
		res.Err = err
	}
	return res
}

// boundedBuffer keeps the first and last OutputLimit/2 bytes written.
type boundedBuffer struct {
	mu      sync.Mutex
	head    bytes.Buffer
	tail    []byte
	dropped int64
}

const half = OutputLimit / 2

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if room := half - b.head.Len(); room > 0 {
		take := room
		if take > len(p) {
			take = len(p)
		}
		b.head.Write(p[:take])
		p = p[take:]
	}
	if len(p) == 0 {
		return n, nil
	}
	b.tail = append(b.tail, p...)
	if over := len(b.tail) - half; over > 0 {
		b.dropped += int64(over)
		b.tail = append([]byte(nil), b.tail[over:]...)
	}
	return n, nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dropped == 0 {
		return b.head.String() + string(b.tail)
	}
	return fmt.Sprintf("%s\n... [%d bytes truncated] ...\n%s", b.head.String(), b.dropped, b.tail)
}
