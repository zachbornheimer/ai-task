package tasks_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/zachbornheimer/ai-task"
)

// TestPublicContract exercises every exported operation through the public
// package so that the facade cannot drift from the internal engine.
func TestPublicContract(t *testing.T) {
	ctx := context.Background()
	eng, err := tasks.Open(ctx, tasks.Config{Path: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	proj, err := eng.InitProject(ctx, "svc", "")
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := eng.Add(ctx, tasks.TaskSpec{ProjectID: proj.ID, Description: "Parse tokens"})
	if err != nil {
		t.Fatal(err)
	}
	b, warnings, err := eng.Add(ctx, tasks.TaskSpec{ProjectID: proj.ID, Description: "Reject expired access tokens", Acceptance: []tasks.AcceptanceCriterion{{Description: "expired -> 401"}}})
	if err != nil || len(warnings) != 0 {
		t.Fatal(err, warnings)
	}
	if err := eng.AddDependency(ctx, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	g, err := eng.Graph(ctx, proj.ID)
	if err != nil || len(g.Edges) != 1 || g.Edges[0] != (tasks.Edge{Task: b.ID, Requires: a.ID}) {
		t.Fatalf("graph %+v %v", g, err)
	}
	sess, err := eng.Take(ctx, tasks.TakeRequest{Project: proj.ID})
	if err != nil || sess.TaskID != a.ID {
		t.Fatalf("take %+v %v", sess, err)
	}
	if _, err := eng.Log(ctx, sess.Token, tasks.LogEntry{Done: "x", Next: "y"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Renew(ctx, sess.Token, 0); err != nil {
		t.Fatal(err)
	}
	res, err := eng.Finish(ctx, sess.Token, tasks.FinishOptions{})
	if err != nil || res.Status != tasks.StatusComplete {
		t.Fatalf("finish %+v %v", res, err)
	}
	view, err := eng.Show(ctx, b.ID)
	if err != nil || view.Status != tasks.StatusAvailable {
		t.Fatalf("show %+v %v", view, err)
	}
	if _, err := eng.Take(ctx, tasks.TakeRequest{Task: &a.ID}); tasks.ErrorCode(err) != "TASK_COMPLETE" {
		t.Fatalf("code %q", tasks.ErrorCode(err))
	}
	if err := eng.RemoveDependency(ctx, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.ParseTaskID(string(a.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.ParseSessionToken(string(sess.Token)); err != nil {
		t.Fatal(err)
	}
	h, err := eng.History(ctx, a.ID, 10, 0)
	if err != nil || h.Total != 1 {
		t.Fatalf("history %+v %v", h, err)
	}
	avail, err := eng.Available(ctx, proj.ID)
	if err != nil || len(avail) != 1 {
		t.Fatalf("available %+v %v", avail, err)
	}
}
