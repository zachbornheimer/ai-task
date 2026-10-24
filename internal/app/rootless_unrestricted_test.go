package app_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/process"
	"github.com/zachbornheimer/ai-task/internal/proof"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// A rootless project has no repository, so its claim workspace is just the
// directory the task was claimed from: confining checks to it guards
// nothing, while proofs of such tasks routinely reach other checkouts and
// config paths on this machine. Rootless checks therefore run unrestricted.
// The proof is a check reading a file under the atask state prefix in HOME,
// a read every sandbox mode denies regardless of where HOME sits: confined
// the check fails, unrestricted it reads the file and passes.
func TestRootlessChecksRunUnrestricted(t *testing.T) {
	if runtime.GOOS != "darwin" || process.Inherited() {
		t.Skip("needs a fresh Seatbelt sandbox on macOS")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not installed")
	}
	if dir := os.Getenv("ATASK_CHECK_DIR"); dir != "" {
		t.Skip("reads outside check confinement; run it outside a check")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("no home directory: %v", err)
	}
	stateDir := filepath.Join(home, ".local", "state", "atask")
	database := filepath.Join(stateDir, "atask.db")
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(database, []byte("unrestricted"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	state, cwd := t.TempDir(), t.TempDir()
	e, err := app.Open(ctx, app.Config{
		Path: filepath.Join(state, "at.db"), Actor: "zach", Cwd: cwd,
		Sandbox: &proof.SandboxConfig{Mode: process.SandboxStrict},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	p, err := e.AddProject(ctx, "Notes")
	if err != nil {
		t.Fatal(err)
	}
	spec := mustParseProof(t, "run: cat "+database)
	res, err := e.Apply(ctx, plan.ChangeSet{ProjectID: p.ID, Operations: []plan.Change{
		plan.AddTask{Title: "read outside the workspace", Proof: spec, Regression: spec},
	}})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Created["#0"]
	s, err := e.Claim(ctx, app.ClaimRequest{TaskID: &id})
	if err != nil {
		t.Fatal(err)
	}
	v, err := e.Verify(ctx, s.Token, verification.ModeComplete)
	if err != nil || !v.Passed {
		t.Fatalf("rootless verify complete must pass unrestricted: err=%v passed=%v summary=%s", err, v.Passed, v.Summary)
	}
}
