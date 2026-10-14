package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// A rootless claim has no worktree to hold its token, so the token lives in
// a SessionStore under the atask state directory, one file per task per
// claim directory. Nothing is ever written into the claim directory itself.

// StoredSession is one token file: the claim directory, the task, the token.
type StoredSession struct {
	Cwd, TaskID, Token string
}

// SessionStore is a directory of StoredSession files.
type SessionStore struct{ Dir string }

const sessionFileFields = 3

func (s SessionStore) path(cwd, taskID string) string {
	sum := sha256.Sum256([]byte(cwd + "\x00" + taskID))
	return filepath.Join(s.Dir, hex.EncodeToString(sum[:]))
}

// Save records the token of taskID claimed from cwd, readable by its owner only.
func (s SessionStore) Save(v StoredSession) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	body := strings.Join([]string{v.Cwd, v.TaskID, v.Token}, "\n") + "\n"
	return os.WriteFile(s.path(v.Cwd, v.TaskID), []byte(body), 0o600)
}

// Find returns the stored session of taskID claimed from cwd.
func (s SessionStore) Find(cwd, taskID string) (StoredSession, bool) {
	return readStoredSession(s.path(cwd, taskID))
}

// ForCwd lists every stored session claimed from cwd.
func (s SessionStore) ForCwd(cwd string) []StoredSession {
	return s.matching(func(v StoredSession) bool { return v.Cwd == cwd })
}

// Remove deletes the stored session of taskID claimed from cwd.
func (s SessionStore) Remove(cwd, taskID string) { _ = os.Remove(s.path(cwd, taskID)) }

// RemoveTask deletes every stored session of taskID, whichever directory it was claimed from.
func (s SessionStore) RemoveTask(taskID string) {
	for _, v := range s.matching(func(v StoredSession) bool { return v.TaskID == taskID }) {
		s.Remove(v.Cwd, v.TaskID)
	}
}

func (s SessionStore) matching(keep func(StoredSession) bool) []StoredSession {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil
	}
	var out []StoredSession
	for _, e := range entries {
		if v, ok := readStoredSession(filepath.Join(s.Dir, e.Name())); ok && keep(v) {
			out = append(out, v)
		}
	}
	return out
}

func readStoredSession(path string) (StoredSession, bool) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return StoredSession{}, false
	}
	f := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(f) != sessionFileFields {
		return StoredSession{}, false
	}
	return StoredSession{Cwd: f[0], TaskID: f[1], Token: f[2]}, true
}
