package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/workspace"
)

// A unix socket path must fit sun_path (104 bytes on macOS). OpenTofu's
// provider plugin socket is $TMPDIR/plugin<10 digits>, so checks that run
// in a deep worktree must still get a TMPDIR short enough to hold it.
const (
	sunPathLimit = 104
	pluginSocket = "/plugin1234567890"
)

func TestCheckEnvTmpdirFitsUnixSocket(t *testing.T) {
	t.Setenv(workspace.CheckTmpRootEnv, "") // the production root, not a test override
	deep := filepath.Join(t.TempDir(), strings.Repeat("w", 90), "worktree-a")
	other := filepath.Join(filepath.Dir(deep), "worktree-b")
	if len(deep) < 150 {
		t.Fatalf("fixture dir is %d characters; the contract is for 150 and up", len(deep))
	}

	tmpFor := func(dir string) string {
		t.Helper()
		got := envValues(checkEnv(dir), "TMPDIR")
		if len(got) != 1 {
			t.Fatalf("checkEnv(%q) sets TMPDIR %d times", dir, len(got))
		}
		if got[0] == os.TempDir() {
			t.Fatalf("checkEnv(%q) fell back to the shared temp dir %s", dir, got[0])
		}
		t.Cleanup(func() { _ = os.RemoveAll(got[0]) })
		return got[0]
	}
	a, b := tmpFor(deep), tmpFor(other)

	if n := len(a + pluginSocket); n >= sunPathLimit {
		t.Errorf("TMPDIR %s plus %s is %d bytes; a unix socket path must be under %d", a, pluginSocket, n, sunPathLimit)
	}
	if a == b {
		t.Errorf("two different check dirs share TMPDIR %s", a)
	}
	if info, err := os.Stat(a); err != nil || !info.IsDir() {
		t.Errorf("TMPDIR %s is not a directory checks can use: %v", a, err)
	}
}
