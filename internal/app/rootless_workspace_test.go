package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/proof"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TestRootlessClaimWorkspaceIsClaimDir files a rootless task from one
// directory and claims it from others: the attempt workspace, and so the
// directory its checks and on-complete actions run in, is the claiming
// directory, not the one the task was filed from.
func TestRootlessClaimWorkspaceIsClaimDir(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "at.db")
	filed, first, second := t.TempDir(), t.TempDir(), t.TempDir()
	open := func(cwd string) *app.Engine {
		t.Helper()
		e, err := app.Open(ctx, app.Config{Path: db, Actor: "zach", Cwd: cwd})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	// The checks pass only where the marker exists (the claiming
	// directories, never the filing one) and make a temp file under the
	// TMPDIR they are given.
	for _, dir := range []string{first, second} {
		if err := os.WriteFile(filepath.Join(dir, "claimed-here"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	e := open(filed)
	p, err := e.AddProject(ctx, "Notes")
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Apply(ctx, plan.ChangeSet{ProjectID: p.ID, Operations: []plan.Change{plan.AddTask{
		Title:      "filed elsewhere",
		Proof:      mustParseProof(t, `run: sh -c 'test -f claimed-here && mktemp "${TMPDIR:?}/proof.XXXXXX"'`),
		Regression: mustParseProof(t, `run: sh -c 'test -f claimed-here && mktemp "${TMPDIR:?}/regression.XXXXXX"'`),
		OnComplete: []proof.ActionSpec{action(t, "run: sh -c 'echo done > action.txt'", false)},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Created["#0"]
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	claimFrom := func(e *app.Engine, want string) app.Session {
		t.Helper()
		s, err := e.Claim(ctx, app.ClaimRequest{TaskID: &id})
		if err != nil {
			t.Fatal(err)
		}
		if s.Workspace != want || s.Task.Attempt == nil || s.Task.Attempt.Workspace != want {
			t.Fatalf("claim workspace %q (attempt %+v), want the claiming dir %q (filed from %q)", s.Workspace, s.Task.Attempt, want, filed)
		}
		v, err := e.Show(ctx, p.ID, string(id), true)
		if err != nil {
			t.Fatal(err)
		}
		if v.Attempt == nil || v.Attempt.Workspace != want {
			t.Fatalf("recorded attempt workspace %+v, want %q", v.Attempt, want)
		}
		return s
	}

	// A directory whose parent is read-only cannot hold <dir>.tmp: the
	// claim is refused up front instead of every check failing later.
	locked := t.TempDir()
	stuck := filepath.Join(locked, "d")
	if err := os.Mkdir(stuck, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	e = open(stuck)
	if _, err := e.Claim(ctx, app.ClaimRequest{TaskID: &id}); fault.CodeOf(err) != fault.CodeInvalidInput || !strings.Contains(err.Error(), "TMPDIR") {
		t.Fatalf("claim without a creatable TMPDIR: want INVALID_INPUT naming TMPDIR, got %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	e = open(first)
	s := claimFrom(e, first)
	for _, mode := range []verification.Mode{verification.ModeProof, verification.ModeRegression} {
		if v, err := e.Verify(ctx, s.Token, mode); err != nil || !v.Passed {
			t.Fatalf("verify %s from the claiming dir: %+v %v", mode, v, err)
		}
	}
	for _, prefix := range []string{"proof.", "regression."} {
		if m, _ := filepath.Glob(filepath.Join(first+".tmp", prefix+"*")); len(m) != 1 {
			t.Fatalf("checks must mktemp under %s.tmp: %s* matched %v", first, prefix, m)
		}
	}
	if err := e.Release(ctx, app.ReleaseRequest{Token: s.Token}); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	// A re-claim after release from another directory moves the workspace.
	e = open(second)
	t.Cleanup(func() { _ = e.Close() })
	s = claimFrom(e, second)
	v, err := e.Verify(ctx, s.Token, verification.ModeComplete)
	if err != nil || !v.Completed {
		t.Fatalf("verify complete from the re-claiming dir: %+v %v", v, err)
	}
	acts, err := e.Show(ctx, p.ID, string(id), true)
	if err != nil {
		t.Fatal(err)
	}
	if acts.Status != task.StatusComplete || len(acts.Actions) != 1 || acts.Actions[0].Status != proof.ActionSucceeded {
		t.Fatalf("want complete with one succeeded action, got %s %+v", acts.Status, acts.Actions)
	}
	if b, err := os.ReadFile(filepath.Join(second, "action.txt")); err != nil || strings.TrimSpace(string(b)) != "done" {
		t.Fatalf("on-complete action did not run in the claiming dir: %q %v", b, err)
	}
	for _, dir := range []string{filed, first} {
		if _, err := os.Stat(filepath.Join(dir, "action.txt")); !os.IsNotExist(err) {
			t.Fatalf("on-complete action ran in %s: %v", dir, err)
		}
	}
}
