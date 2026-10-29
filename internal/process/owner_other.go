//go:build !windows && !darwin && !linux

package process

// startTime is unavailable on this platform: an owner is judged by pid alone.
func startTime(int) (string, bool) { return "", false }
