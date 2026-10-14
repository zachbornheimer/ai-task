package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/must"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TestEpicRunReconciledAfterVerifierDies: an epic verifier that dies
// leaves a running run behind. Once its job lease expires the run is
// closed as an error that says so, the epic is pending again (not failed,
// not stuck "running"), and a later worker reruns it to completion.
func TestEpicRunReconciledAfterVerifierDies(t *testing.T) {
	f := newGitFixture(t)
	flag := filepath.Join(t.TempDir(), "go")
	res := f.apply(
		plan.AddGroup{Key: "epic", Title: "Epic", TaskChecks: []verification.CheckSpec{check("e2e", "while [ ! -f "+flag+" ]; do sleep 0.05; done; test -f a.txt", true)}},
		plan.AddTask{Key: "a", Title: "a", Parent: "epic", TaskChecks: []verification.CheckSpec{check("unit-a", "test -f a.txt", true)}},
	)
	g, a := res.Created["epic"], res.Created["a"]
	s := f.claim(string(a))
	f.commit(s.Workspace, "a.txt", "x")
	f.complete(s)

	// The verifier starts the epic run, then its process dies.
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan error, 1)
	go func() { _, err := f.e.RunPendingGroups(ctx, f.proj.ID, ""); done <- err }()
	deadline := time.Now().Add(15 * time.Second)
	for f.show(string(g)).GroupVerification.Status != "running" {
		if time.Now().After(deadline) {
			t.Fatal("epic never started running")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	must.OK(t, os.WriteFile(flag, []byte("1"), 0o600))
	f.c.Advance(2 * time.Hour) // the verifier's lease expires
	if _, err := f.e.Summary(f.ctx, f.proj.ID); err != nil {
		t.Fatal(err)
	}
	// The epic's latest run is the dead verifier's: closed (finished, with
	// a summary saying so), so the epic reads as pending, not running.
	v := f.show(string(g))
	gv := v.GroupVerification
	if gv.Status != "pending" || gv.At == nil || !strings.Contains(gv.Summary, "verifier") || v.CompletedAt != nil {
		t.Fatalf("the dead verifier's run must be closed and the epic pending: %+v", gv)
	}
	deadRun := gv.RunID

	// A later worker reruns the epic.
	if ran, err := f.open().RunPendingGroups(f.ctx, f.proj.ID, ""); !ran || err != nil {
		t.Fatalf("rerun: ran=%v err=%v", ran, err)
	}
	if v := f.show(string(g)); v.GroupVerification.Status != "passed" || v.GroupVerification.RunID == deadRun || v.CompletedAt == nil {
		t.Fatalf("after rerun: %+v", v.GroupVerification)
	}
}
