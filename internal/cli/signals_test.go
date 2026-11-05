//go:build unix

package cli

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"
)

const signalTestWait = 10 * time.Second

// TestTerminationSignalAbandonsTheRun: a SIGTERM (kill, a timeout wrapper
// giving up) or SIGHUP (the terminal closing) during a verification stops
// the work and then closes its run, using a context the signal has not
// canceled. Without it the process dies leaving a run recorded running
// until the reaper notices.
func TestTerminationSignalAbandonsTheRun(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			started := make(chan struct{})
			var abandonErr error
			abandoned := false
			work := func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			}
			abandon := func(ctx context.Context) {
				abandoned, abandonErr = true, ctx.Err()
			}
			done := make(chan error, 1)
			go func() { done <- runUntilTerminated(context.Background(), work, abandon) }()
			select {
			case <-started:
			case <-time.After(signalTestWait):
				t.Fatal("the verification never started")
			}
			if err := syscall.Kill(syscall.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err = %v, want the work's cancellation", err)
				}
			case <-time.After(signalTestWait):
				t.Fatalf("%s did not end the verification", sig)
			}
			if !abandoned {
				t.Fatal("the run was not abandoned after the signal")
			}
			if abandonErr != nil {
				t.Fatalf("abandon ran on a dead context: %v", abandonErr)
			}
		})
	}
}

// TestFinishedWorkIsNotAbandoned: no signal, no cleanup.
func TestFinishedWorkIsNotAbandoned(t *testing.T) {
	abandoned := false
	err := runUntilTerminated(context.Background(), func(context.Context) error { return nil }, func(context.Context) { abandoned = true })
	if err != nil || abandoned {
		t.Fatalf("err = %v, abandoned = %v, want a clean finish", err, abandoned)
	}
}
