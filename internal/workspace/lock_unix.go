//go:build !windows

package workspace

import (
	"errors"
	"os"
	"syscall"
)

// tryLockFile takes an exclusive advisory lock on f without blocking. The
// lock belongs to the descriptor and is released by unlockFile, by closing
// f, or by the kernel when the process dies.
func tryLockFile(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return false, err
}

func unlockFile(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
