//go:build windows

package process

// pidExists cannot probe a process here; a run's heartbeat governs its
// liveness instead.
func pidExists(int) (exists, probed bool) { return false, false }

func startTime(int) (string, bool) { return "", false }
