package app_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/process"
	"github.com/zachbornheimer/ai-task/internal/proof"
)

const sshProof = "run: ssh -J mini zbornheimer@backend.ai.lan 'grep -c conversation ~/x.ts'"

func needsSeatbelt(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("checks are confined only on macOS")
	}
}

func openEngine(t *testing.T, mode process.SandboxMode) *app.Engine {
	t.Helper()
	e, err := app.Open(context.Background(), app.Config{
		Path: filepath.Join(t.TempDir(), "at.db"), Actor: "zach", Cwd: t.TempDir(),
		Sandbox: &proof.SandboxConfig{Mode: mode},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func grantExtraEnv(t *testing.T, names ...string) {
	t.Helper()
	cfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "atask"), 0o750); err != nil {
		t.Fatal(err)
	}
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = `"` + n + `"`
	}
	toml := "[sandbox]\nextra_env = [" + strings.Join(quoted, ", ") + "]\n"
	if err := os.WriteFile(filepath.Join(cfg, "atask", "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", cfg)
}

// A proof that logs in over ssh cannot pass in the check sandbox (it denies
// reads of ~/.ssh), so it must be refused when filed, naming the need.
func TestFilingRefusesAProofThatNeedsAnSSHIdentity(t *testing.T) {
	needsSeatbelt(t)
	f := newGitFixture(t)

	_, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
		plan.AddTask{Title: "remote box carries the fix", Proof: mustParseProof(t, sshProof), Regression: mustParseProof(t, "run: git status")},
	}})
	wantCode(t, err, fault.CodeInvalidInput)
	if !strings.Contains(err.Error(), "~/.ssh") {
		t.Fatalf("refusal must name the grant it needs: %v", err)
	}

	_, err = f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
		plan.AddTask{Title: "remote regression", Proof: mustParseProof(t, "run: git status"), Regression: mustParseProof(t, sshProof)},
	}})
	wantCode(t, err, fault.CodeInvalidInput)
}

// Rewriting an existing contract goes through the same gate.
func TestUpdatingAContractToNeedSSHIsRefused(t *testing.T) {
	needsSeatbelt(t)
	f := newGitFixture(t)
	id := f.add("t")

	_, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
		plan.UpdateTask{Target: plan.Ref(id), Proof: mustParseProof(t, sshProof)},
	}})
	wantCode(t, err, fault.CodeInvalidInput)
	if !strings.Contains(err.Error(), "~/.ssh") {
		t.Fatalf("refusal must name the grant it needs: %v", err)
	}
}

// The refusal exists because the sandbox confines the check. Where checks
// run unconfined (a rootless project, or [sandbox] mode = "none") ssh works,
// so filing it must stay allowed.
func TestSSHProofIsFiledWhereChecksRunUnconfined(t *testing.T) {
	needsSeatbelt(t)
	ctx := context.Background()
	spec := mustParseProof(t, sshProof)
	reg := mustParseProof(t, "run: git status")

	rootless := openEngine(t, process.SandboxStrict)
	p, err := rootless.AddProject(ctx, "Notes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rootless.Apply(ctx, plan.ChangeSet{ProjectID: p.ID, Operations: []plan.Change{plan.AddTask{Title: "ssh", Proof: spec, Regression: reg}}}); err != nil {
		t.Fatalf("rootless project must file an ssh proof: %v", err)
	}

	open := newGitFixture(t)
	unconfined := openEngine(t, process.SandboxNone)
	gp, err := unconfined.InitProject(ctx, "unconfined", open.repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unconfined.Apply(ctx, plan.ChangeSet{ProjectID: gp.ID, Operations: []plan.Change{plan.AddTask{Title: "ssh", Proof: spec, Regression: reg}}}); err != nil {
		t.Fatalf("mode none must file an ssh proof: %v", err)
	}
}

// op is refused until the owner passes the service-account token through
// extra_env; with the grant it is a headless login and is filed.
func TestOPProofNeedsTheTokenGrant(t *testing.T) {
	needsSeatbelt(t)
	f := newGitFixture(t)
	file := func() error {
		_, err := f.e.Apply(f.ctx, plan.ChangeSet{ProjectID: f.proj.ID, Operations: []plan.Change{
			plan.AddTask{Title: "reads the key", Proof: mustParseProof(t, "run: op read op://Zysys/E2B-API-Key/credential"), Regression: mustParseProof(t, "run: git status")},
		}})
		return err
	}

	err := file()
	wantCode(t, err, fault.CodeInvalidInput)
	if !strings.Contains(err.Error(), proof.OPTokenEnv) {
		t.Fatalf("refusal must name the extra_env entry it needs: %v", err)
	}

	grantExtraEnv(t, proof.OPTokenEnv)
	if err := file(); err != nil {
		t.Fatalf("op with %s granted must be filed: %v", proof.OPTokenEnv, err)
	}
}
