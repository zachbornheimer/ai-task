package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// CheckTmpRootEnv names the directory under which checks' private TMPDIRs
// are made. Unset, it is a short per-user directory under /tmp.
const CheckTmpRootEnv = "ATASK_CHECK_TMP_ROOT"

// checkTmpHashBytes is how much of the SHA-256 of a check dir names its
// TMPDIR: 8 bytes, 16 hex characters, is collision-free for any number of
// worktrees one machine holds.
const checkTmpHashBytes = 8

// checkTmpRoot is deliberately short and independent of the worktree
// location: a unix socket path (macOS sun_path is 104 bytes) made inside a
// check's TMPDIR must fit however deep the worktree is.
func checkTmpRoot() string {
	if root := os.Getenv(CheckTmpRootEnv); root != "" {
		return root
	}
	return fmt.Sprintf("/tmp/atask-%d", os.Getuid())
}

// CheckTmp is the TMPDIR checks running in dir get: one directory per
// dir, named by a hash of it so its length never depends on dir's. It
// owns the path; EnsureCheckTmp creates it and RemoveCheckTmp removes it,
// so creation and teardown cannot name different directories.
func CheckTmp(dir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(dir)))
	return filepath.Join(checkTmpRoot(), hex.EncodeToString(sum[:checkTmpHashBytes]))
}

// EnsureCheckTmp creates the TMPDIR for checks running in dir and returns
// it. The root sits in a shared directory, so a root that is not a private
// directory (a symlink or group/world-accessible directory another user
// planted) is refused rather than handed to checks.
func EnsureCheckTmp(dir string) (string, error) {
	tmp := CheckTmp(dir)
	root := filepath.Dir(tmp)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is not a private directory (mode %v)", root, info.Mode())
	}
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return "", err
	}
	return tmp, nil
}

// RemoveCheckTmp removes the TMPDIR checks running in dir had.
func RemoveCheckTmp(dir string) {
	_ = os.RemoveAll(CheckTmp(dir))
}
