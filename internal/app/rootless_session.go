package app

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

// DefaultSessionRoot places rootless claim tokens beside the database, or
// in the local state directory when the database is remote.
func DefaultSessionRoot(dbPath string) string {
	if sqlite.IsPostgresURL(dbPath) {
		return filepath.Join(localStateRoot(), "sessions")
	}
	if dbPath == ":memory:" {
		return filepath.Join(os.TempDir(), "atask-sessions")
	}
	return filepath.Join(filepath.Dir(dbPath), "sessions")
}

func (e *Engine) sessions() workspace.SessionStore {
	return workspace.SessionStore{Dir: DefaultSessionRoot(e.store.Path())}
}

// live reports whether the stored token still owns a live claim.
func (e *Engine) live(ctx context.Context, tok execution.Token) bool {
	now := e.now()
	live := false
	_ = e.store.Read(ctx, func(tx *sqlite.Tx) error {
		auth, err := tx.AttemptByDigest(tok.Digest())
		if err != nil {
			return err
		}
		live = execution.Authorize(auth.Attempt, auth.CurrentSeq, now) == nil
		return nil
	})
	return live
}

// liveSessions lists the stored sessions of this engine's cwd whose claim is
// still live; dead ones are deleted.
func (e *Engine) liveSessions(ctx context.Context) []workspace.StoredSession {
	var out []workspace.StoredSession
	for _, v := range e.sessions().ForCwd(e.cwd) {
		if e.live(ctx, execution.Token(v.Token)) {
			out = append(out, v)
		} else {
			e.sessions().Remove(v.Cwd, v.TaskID)
		}
	}
	return out
}

// AmbientToken returns the token of the one live rootless claim made from
// this engine's directory, or "" when there is none. Several claims from
// one directory are ambiguous: the caller must pass the token.
func (e *Engine) AmbientToken(ctx context.Context) (execution.Token, error) {
	if e.cwd == "" {
		return "", nil
	}
	found := e.liveSessions(ctx)
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return execution.Token(found[0].Token), nil
	}
	ids := make([]string, len(found))
	for i, v := range found {
		ids[i] = v.TaskID
	}
	sort.Strings(ids)
	return "", fault.New(fault.CodeInvalidInput, "several claims were made from this directory; pass the token (tasks: %s)", strings.Join(ids, ", "))
}

// liveClaimFromCwd returns the live claim of id made from this directory.
func (e *Engine) liveClaimFromCwd(ctx context.Context, id task.ID) (Session, bool) {
	v, ok := e.sessions().Find(e.cwd, string(id))
	if !ok || !e.live(ctx, execution.Token(v.Token)) {
		return Session{}, false
	}
	tok := execution.Token(v.Token)
	view, err := e.Whoami(ctx, tok)
	if err != nil || view.Attempt == nil {
		return Session{}, false
	}
	s := Session{Task: view, Token: tok, AttemptSeq: view.Attempt.Seq, LeaseUntil: view.Attempt.LeaseExpiresAt, Resumed: view.Attempt.Seq > 1, Workspace: view.Attempt.Workspace}
	if view.Handoff != nil {
		s.Handoff = *view.Handoff
	}
	return s, true
}

// storeRootlessToken records the token of a rootless claim under the state
// directory, replacing any earlier claim's file for the same task (takeover).
func (e *Engine) storeRootlessToken(s Session) error {
	e.sessions().RemoveTask(string(s.Task.ID))
	return e.sessions().Save(workspace.StoredSession{Cwd: e.cwd, TaskID: string(s.Task.ID), Token: string(s.Token)})
}
