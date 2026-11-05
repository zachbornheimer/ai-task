package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
)

// keepClaimWindow renews a claim's provisional lease while it provisions the
// worktree it must store before it can be worked on, and returns the function
// that stops the renewal.
//
// A claim records its attempt before it builds the worktree and stores the
// token in that worktree last, so the attempt's first expiry is the claim
// window rather than the lease: the window is how long a claim whose process
// died may hold a task nobody can work on. Provisioning copies the source
// checkout's ignored dependencies (node_modules, built artifacts) file by
// file, which takes longer than that window on a busy host, and a claim that
// outlives it ends mid-copy — its own store is then refused with "attempt
// already ended" although it is alive and working. Renewing keeps the task
// while the provisioning process is alive, and still returns it within one
// window of the process dying, because a renewal only happens in the process
// that owns the claim.
func (e *Engine) keepClaimWindow(ctx context.Context, token execution.Token) (stop func()) {
	var once sync.Once
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		interval := min(e.heartbeatEvery, execution.ClaimWindow/3)
		t := time.NewTimer(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-quit:
				return
			case <-t.C:
			}
			if err := e.renewClaimWindow(ctx, token); err != nil {
				if terminalAuthority(err) || errors.Is(err, context.Canceled) {
					return
				}
				// Transient, typically a busy database: retry sooner than
				// the interval so the window never runs out under retries.
				t.Reset(interval / 5)
				continue
			}
			t.Reset(interval)
		}
	}()
	return func() {
		once.Do(func() { close(quit) })
		<-done
	}
}

// renewClaimWindow re-arms a provisional attempt's claim window, keeping its
// recorded expiry one window ahead while the claim provisions its worktree.
func (e *Engine) renewClaimWindow(ctx context.Context, token execution.Token) error {
	now := e.now()
	return e.store.Write(ctx, func(tx *sqlite.Tx) error {
		a, err := e.authorize(tx, token, now)
		if err != nil {
			return err
		}
		return tx.RenewLease(a.ID, now.Add(execution.ClaimWindow))
	})
}
