package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// rootlessEngine opens an engine whose cwd is a fresh temp dir, with two
// claimable tasks in a rootless project.
func rootlessEngine(t *testing.T) (*app.Engine, string, string, []task.ID) {
	t.Helper()
	ctx := context.Background()
	state, cwd := t.TempDir(), t.TempDir()
	e, err := app.Open(ctx, app.Config{Path: filepath.Join(state, "at.db"), Actor: "zach", Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	p, err := e.AddProject(ctx, "Notes")
	if err != nil {
		t.Fatal(err)
	}
	var ops []plan.Change
	for _, title := range []string{"first note", "second note"} {
		ops = append(ops, plan.AddTask{Title: title, Proof: mustParseProof(t, "run: go version"), Regression: mustParseProof(t, "run: go version")})
	}
	res, err := e.Apply(ctx, plan.ChangeSet{ProjectID: p.ID, Operations: ops})
	if err != nil {
		t.Fatal(err)
	}
	return e, state, cwd, []task.ID{res.Created["#0"], res.Created["#1"]}
}

func sessionFiles(t *testing.T, state string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(state, "sessions"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return entries
}

func TestRootlessClaimTokenLoadsFromCwd(t *testing.T) {
	ctx := context.Background()
	e, state, cwd, ids := rootlessEngine(t)
	s, err := e.Claim(ctx, app.ClaimRequest{TaskID: &ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	files := sessionFiles(t, state)
	if len(files) != 1 {
		t.Fatalf("want one session file, got %d", len(files))
	}
	info, err := files[0].Info()
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("session file mode %v err %v", info.Mode(), err)
	}
	if left, _ := os.ReadDir(cwd); len(left) != 0 {
		t.Fatalf("claim wrote into cwd: %v", left)
	}

	tok, err := e.AmbientToken(ctx)
	if err != nil || tok != s.Token {
		t.Fatalf("ambient token %q err %v, want %q", tok, err, s.Token)
	}
	if _, err := e.Log(ctx, tok, execution.LogEntry{Note: "from cwd"}); err != nil {
		t.Fatalf("log: %v", err)
	}
	if v, err := e.Verify(ctx, tok, verification.ModeComplete); err != nil || !v.Passed {
		t.Fatalf("verify complete: %+v %v", v, err)
	}

	// Repeat claim of a live claim returns it.
	e2, state2, _, ids2 := rootlessEngine(t)
	first, err := e2.Claim(ctx, app.ClaimRequest{TaskID: &ids2[0]})
	if err != nil {
		t.Fatal(err)
	}
	again, err := e2.Claim(ctx, app.ClaimRequest{TaskID: &ids2[0]})
	if err != nil || again.Token != first.Token || again.AttemptSeq != first.AttemptSeq {
		t.Fatalf("repeat claim: %+v %v, want token %q seq %d", again, err, first.Token, first.AttemptSeq)
	}
	if err := e2.Release(ctx, app.ReleaseRequest{Token: first.Token}); err != nil {
		t.Fatal(err)
	}
	if n := len(sessionFiles(t, state2)); n != 0 {
		t.Fatalf("release left %d session files", n)
	}
	if tok, err := e2.AmbientToken(ctx); err != nil || tok != "" {
		t.Fatalf("after release: token %q err %v", tok, err)
	}
}

func TestRootlessClaimsFromOneCwdNeedExplicitToken(t *testing.T) {
	ctx := context.Background()
	e, _, _, ids := rootlessEngine(t)
	for i := range ids {
		if _, err := e.Claim(ctx, app.ClaimRequest{TaskID: &ids[i]}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := e.AmbientToken(ctx)
	if fault.CodeOf(err) != fault.CodeInvalidInput || !strings.Contains(err.Error(), "several claims were made from this directory; pass the token") {
		t.Fatalf("want several-claims INVALID_INPUT, got %v", err)
	}
	for _, id := range ids {
		if !strings.Contains(err.Error(), string(id)) {
			t.Fatalf("error must list %s: %v", id, err)
		}
	}
}
