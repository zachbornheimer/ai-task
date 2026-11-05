package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// terminationSignals are the ones that end a verification the way a kill, a
// timeout wrapper, or a closing terminal does. SIGINT keeps its default.
var terminationSignals = []os.Signal{syscall.SIGTERM, syscall.SIGHUP}

// abandonBudget bounds the cleanup after a signal: the process is already
// being asked to leave, so closing the run gets seconds, not minutes.
const abandonBudget = 10 * time.Second

// runUntilTerminated runs work under a context that a termination signal
// cancels. When a signal did, abandon runs on a fresh, bounded context (the
// signal has already canceled work's), so the process can record that its
// run is lost instead of leaving it recorded running for the reaper.
func runUntilTerminated(ctx context.Context, work func(context.Context) error, abandon func(context.Context)) error {
	sigCtx, stop := signal.NotifyContext(ctx, terminationSignals...)
	defer stop()
	err := work(sigCtx)
	if sigCtx.Err() != nil && ctx.Err() == nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), abandonBudget)
		defer cancel()
		abandon(cleanup)
	}
	return err
}
