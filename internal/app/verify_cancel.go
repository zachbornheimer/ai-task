package app

import (
	"context"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
)

// Why a run was closed from outside. Both carry the abandoned prefix so
// they read like any run lost with its verifier: neither counts toward the
// attempt budget.
const (
	canceledRun   = sqlite.AbandonedPrefix + " canceled by the session that owns it (verify cancel)"
	terminatedRun = sqlite.AbandonedPrefix + " the verifying process was terminated by a signal (SIGTERM or SIGHUP)"
)

// CancelResult says how many running verification runs a cancel closed.
type CancelResult struct {
	Closed  int64  `json:"closed"`
	Message string `json:"message"`
}

// CancelVerify force-closes every verification run still recorded as
// running for the session's attempt, so a `verify` whose caller gave up
// but whose process lingers does not hold the task in verifying until the
// reaper notices. The lingering process finds its run closed at its next
// fence and stops without completing anything; the claim stays live and no
// failure is charged. Closing nothing is not an error.
func (e *Engine) CancelVerify(ctx context.Context, token execution.Token) (CancelResult, error) {
	return e.closeRunning(ctx, token, canceledRun, "verification canceled; the claim is still live")
}

// AbandonVerify closes the session's running runs as lost to a termination
// signal. The CLI calls it, on a context the signal did not cancel, after
// SIGTERM or SIGHUP stopped its own verification.
func (e *Engine) AbandonVerify(ctx context.Context, token execution.Token) (CancelResult, error) {
	return e.closeRunning(ctx, token, terminatedRun, "verification stopped by a signal; the claim is still live")
}

func (e *Engine) closeRunning(ctx context.Context, token execution.Token, summary, closedMessage string) (CancelResult, error) {
	now := e.now()
	var closed int64
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		a, err := e.authorize(tx, token, now)
		if err != nil {
			return err
		}
		closed, err = tx.CancelRunningRuns(a.ID, summary, now)
		return err
	})
	if err != nil {
		return CancelResult{}, err
	}
	if closed > 0 {
		e.notify()
		return CancelResult{Closed: closed, Message: closedMessage}, nil
	}
	return CancelResult{Message: "no verification was running for this session"}, nil
}
