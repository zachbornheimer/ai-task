package workspace

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// heardLines collects what a Manager reports to Progress and, once the first
// line arrives, ends the step that is waiting: the tests wait for the report,
// never for a clock.
type heardLines struct {
	mu     sync.Mutex
	got    []string
	cancel context.CancelFunc
}

func (h *heardLines) add(s string) {
	h.mu.Lock()
	h.got = append(h.got, s)
	h.mu.Unlock()
	h.cancel()
}

func (h *heardLines) joined() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.got, "\n")
}

// listen returns a context that ends at the first reported line, or after a
// ceiling that only a broken Manager reaches.
func (h *heardLines) listen(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	h.cancel = cancel
	return ctx, cancel
}

// hangingSwitcher is a `wt switch` blocked behind a git hook until its
// context ends.
type hangingSwitcher struct{}

func (hangingSwitcher) Ensure(ctx context.Context, _, _, _ string, _ bool) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// A claim stuck building its worktree is never silent: Progress hears what it
// waits on while it waits, and the switch ends with its context.
func TestSlowWorktreeBuildSaysWhatItWaitsOn(t *testing.T) {
	r := newRepo(t)
	var heard heardLines
	ctx, cancel := heard.listen(r.ctx)
	defer cancel()
	r.mgr.Switch = hangingSwitcher{}
	r.mgr.Progress = heard.add
	r.mgr.NoticeEvery = 10 * time.Millisecond

	if _, err := r.mgr.EnsureTask(ctx, "at-slow", "main", 0); err == nil {
		t.Fatal("a worktree build that never finished must report an error")
	}
	if got := heard.joined(); !strings.Contains(got, "post-checkout hooks") || !strings.Contains(got, "atask/at-slow") {
		t.Fatalf("Progress heard %q, want a line naming the worktree being created and the hooks it waits on", got)
	}
}

// A claim queued behind another process's worktree lock says who holds it
// instead of waiting silently.
func TestWorktreeLockWaitNamesTheHolder(t *testing.T) {
	r := newRepo(t)
	var heard heardLines
	ctx, cancel := heard.listen(r.ctx)
	defer cancel()
	r.mgr.Progress = heard.add
	r.mgr.NoticeEvery = 10 * time.Millisecond

	release, err := r.mgr.repoLock(r.ctx, worktreeLockFile, "worktree")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if _, err := r.mgr.EnsureTask(ctx, "at-queued", "main", 0); err == nil {
		t.Fatal("a claim that never got the lock must report an error")
	}
	if got := heard.joined(); !strings.Contains(got, "waiting for the worktree lock") || !strings.Contains(got, "held by pid") {
		t.Fatalf("Progress heard %q, want a line naming the lock and its holder", got)
	}
}
