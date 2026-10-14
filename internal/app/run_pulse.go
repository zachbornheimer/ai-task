package app

import (
	"context"
	"time"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
)

// heartbeatWithPulse is heartbeat plus a run pulse: while the run executes
// it stamps the run's heartbeat every renewal interval, so a run whose
// process died is recognizably stale (see Tx.ReconcileRuns).
func (e *Engine) heartbeatWithPulse(ctx context.Context, token execution.Token, lease time.Duration, runID int64) (context.Context, func() error) {
	opCtx, stopLease := e.heartbeat(ctx, token, lease)
	pctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(e.renewalInterval(lease))
		defer t.Stop()
		for {
			select {
			case <-pctx.Done():
				return
			case <-t.C:
				// A failed stamp is retried on the next tick; a run
				// unseen for two intervals is what reaping acts on.
				_ = e.store.Write(pctx, func(tx *sqlite.Tx) error { return tx.TouchRun(runID, e.now()) })
			}
		}
	}()
	return opCtx, func() error { cancel(); <-done; return stopLease() }
}
