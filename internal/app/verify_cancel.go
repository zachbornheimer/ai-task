package app

import (
	"context"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
)

// canceledRun is the summary a canceled run is closed with. It carries the
// abandoned prefix so it reads like any run lost with its verifier: it
// never counts toward the attempt budget.
const canceledRun = sqlite.AbandonedPrefix + " canceled by the session that owns it (verify cancel)"

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
	now := e.now()
	var closed int64
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		a, err := e.authorize(tx, token, now)
		if err != nil {
			return err
		}
		closed, err = tx.CancelRunningRuns(a.ID, canceledRun, now)
		return err
	})
	if err != nil {
		return CancelResult{}, err
	}
	if closed > 0 {
		e.notify()
		return CancelResult{Closed: closed, Message: "verification canceled; the claim is still live"}, nil
	}
	return CancelResult{Message: "no verification was running for this session"}, nil
}
