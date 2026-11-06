package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/testgit"
)

// `wt switch` runs `git worktree add`, which runs the repository's
// post-checkout hooks, and those can take minutes. When the context that
// bounds the switch ends, everything the switch started ends with it: a
// descendant that keeps wt's output pipe open must not hold Ensure past it.
func TestWorktrunkSwitcherEnsureEndsWithItsContext(t *testing.T) {
	bin := t.TempDir()
	// The stand-in for wt starts a background descendant that inherits its
	// output pipe and waits for it: the shape of a slow post-checkout hook.
	testgit.WriteExecutable(t, filepath.Join(bin, "wt"), "#!/bin/sh\nsleep 20 &\nwait\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := WorktrunkSwitcher{}.Ensure(ctx, t.TempDir(), "atask/at-bounded", "main", true)
	if err == nil {
		t.Fatal("a switch that never finished must report an error")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Ensure returned after %s although its context ended after 300ms: the switch's descendants held it", took)
	}
}
