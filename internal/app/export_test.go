package app

// SetFaultHook installs a crash-injection hook: when it returns a non-nil
// error for a point, the operation stops there as a killed process would.
func SetFaultHook(e *Engine, f func(point string) error) { e.faults = f }
