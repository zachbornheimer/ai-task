//go:build windows

package process

import "os/exec"

// isolate keeps the default behaviour: cancellation kills the process
// itself; there is no process group to signal.
func isolate(*exec.Cmd) {}
