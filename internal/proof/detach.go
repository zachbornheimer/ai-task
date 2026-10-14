package proof

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/process"
)

// Start spawns spec in workdir and does not wait for it: the process gets
// its own session and no inherited streams, so it outlives the caller. The
// RunResult records the start time and pid; ReturnCode stays nil because no
// exit status is known.
func (r *Runner) Start(ct CheckType, spec ProofSpec, workdir string) RunResult {
	res := newRunResult(ct, spec, 0)
	argv, err := r.argv(spec, workdir)
	if err != nil {
		res.Output = err.Error()
		return res
	}
	cmd := process.Command(context.Background(), workdir, argv[0], argv[1:]...)
	cmd.SysProcAttr = detachedProcAttr()
	if devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		defer discard.CloseReadOnly(devnull)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	}
	res.StartedAt = time.Now().UTC()
	if err := cmd.Start(); err != nil {
		res.Output = "could not start: " + err.Error()
		return res
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }() // reap while this process lives
	res.Passed = true
	res.Output = fmt.Sprintf("detached (pid %d)", pid)
	return res
}
