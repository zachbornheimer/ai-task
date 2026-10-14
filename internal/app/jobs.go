package app

import (
	"context"
	"errors"
	"time"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
)

// Retry backoff after a verification job that ended in an environment
// error: 30s, 1m, 2m, ... capped at 30m, counted over consecutive error
// jobs.
const (
	jobBackoffBase = 30 * time.Second
	jobBackoffCap  = 30 * time.Minute
)

// jobBackoff returns when the named job may be retried and after
// consecutive environment errors (the zero time when there are none).
func jobBackoff(tx *sqlite.Tx, pid project.ID, name string) (time.Time, error) {
	jobs, err := tx.RecentJobs(pid, name, 16)
	if err != nil {
		return time.Time{}, err
	}
	n := 0
	for i := range jobs {
		j := &jobs[i]
		if j.Status != "error" || j.FinishedAt == nil {
			break
		}
		n++
	}
	if n == 0 {
		return time.Time{}, nil
	}
	d := jobBackoffBase << uint(n-1)
	if n > 12 || d > jobBackoffCap {
		d = jobBackoffCap
	}
	return jobs[0].FinishedAt.Add(d), nil
}

// jobLease identifies the durable job whose lease a verifier renews.
type jobLease struct {
	id    int64
	owner execution.Token
}

// jobHeartbeat renews the verifier's job lease while checks run. Like
// heartbeat, it returns the context check processes must use: lost
// ownership cancels it at once; a transient error is retried sooner.
func (e *Engine) jobHeartbeat(ctx context.Context, job jobLease) (context.Context, func()) {
	opCtx, cancelOp := context.WithCancel(ctx)
	hctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		interval := e.heartbeatEvery
		t := time.NewTimer(interval)
		defer t.Stop()
		for {
			select {
			case <-hctx.Done():
				return
			case <-t.C:
			}
			err := e.store.Write(hctx, func(tx *sqlite.Tx) error {
				return tx.RenewJob(job.id, job.owner.Digest(), e.now().Add(execution.DefaultLease))
			})
			switch {
			case err == nil:
				t.Reset(interval)
			case errors.Is(err, context.Canceled):
				return
			case terminalAuthority(err):
				cancelOp()
				return
			default:
				t.Reset(interval / 5)
			}
		}
	}()
	return opCtx, func() { cancel(); <-done; cancelOp() }
}
