package app

import (
	"context"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
)

// RenewRuntime renews all live rootless claims bound to the calling runtime.
// It never adopts unbound claims or revives terminal attempts. Each mutation
// uses Renew's transactional fencing; infrastructure errors stop the scan.
func (e *Engine) RenewRuntime(ctx context.Context) (int, error) {
	if e.runtime == nil {
		return 0, nil
	}
	candidates, err := e.sessions().AllChecked()
	if err != nil {
		return 0, fault.WrapInternal(err, "read runtime claim tokens")
	}
	renewed := 0
	for _, v := range candidates {
		if v.Runtime == nil || !e.runtime.Allows(v.Runtime) {
			continue
		}
		if _, err := e.Renew(ctx, execution.Token(v.Token)); err != nil {
			if fault.CodeOf(err) == fault.CodeInvalidSession || fault.CodeOf(err) == fault.CodeNotFound {
				e.sessions().Remove(v.Cwd, v.TaskID)
				continue
			}
			return renewed, err
		}
		renewed++
	}
	return renewed, nil
}
