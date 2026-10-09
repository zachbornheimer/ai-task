package app_test

import (
	"context"
	"time"
)

// interruptWhenVerifying starts fn (a verification) with a cancellable
// context, waits until the task reports the wanted status, then cancels
// and returns fn's error. It replaces fixed sleeps, which are flaky under
// the race detector where Git operations are slow.
func (f *fixture) interruptWhenVerifying(ref string, want string, fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan error, 1)
	go func() { done <- fn(ctx) }()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if string(f.status(ref)) == want {
			break
		}
		select {
		case err := <-done:
			cancel()
			f.t.Fatalf("verification finished before reaching %s: %v", want, err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if string(f.status(ref)) != want {
		cancel()
		f.t.Fatalf("task never reached %s", want)
	}
	cancel()
	return <-done
}
