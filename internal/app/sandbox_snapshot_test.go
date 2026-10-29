package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/process"
	"github.com/zachbornheimer/ai-task/internal/proof"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

func TestSandboxedCheckCanUseSnapshotTmp(t *testing.T) {
	if runtime.GOOS != "darwin" || process.Inherited() {
		t.Skip("needs a fresh Seatbelt sandbox on macOS")
	}
	for _, tool := range []string{"sandbox-exec", "sqlite3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	state := filepath.Join(home, ".local", "state", "atask")
	snapshot := filepath.Join(state, "workspaces", "proj", "snap")
	if err := os.MkdirAll(snapshot, 0o750); err != nil {
		t.Fatal(err)
	}
	tmp, err := workspace.EnsureCheckTmp(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(state, "atask.db")
	if err := os.WriteFile(database, []byte("dummy"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "HOME="+home, "TMPDIR="+tmp)
	sb := sandboxPolicy(proof.SandboxConfig{Mode: process.SandboxStrict})(snapshot, env)
	run := func(script string) process.Result {
		return process.Run(context.Background(), process.Spec{
			Argv: []string{"sh", "-c", script}, Dir: snapshot, Env: env, Timeout: time.Minute, Sandbox: sb,
		})
	}

	r := run(`cd "$TMPDIR" && sqlite3 t.db "create table x(a); insert into x values(1); select * from x;"`)
	if r.ExitCode != 0 || strings.TrimSpace(r.Stdout) != "1" {
		t.Fatalf("sqlite in snapshot TMPDIR failed: %+v", r)
	}
	r = run(`cat "` + database + `"`)
	if r.ExitCode == 0 || strings.Contains(r.Stdout, "dummy") {
		t.Fatalf("atask.db read was not denied: %+v", r)
	}
}
