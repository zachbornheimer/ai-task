package app_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func benchEngine(b *testing.B) (*app.Engine, project.Project) {
	b.Helper()
	e, err := app.Open(context.Background(), app.Config{Path: filepath.Join(b.TempDir(), "bench.db")})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { e.Close() })
	p, err := e.InitProject(context.Background(), "bench", "")
	if err != nil {
		b.Fatal(err)
	}
	return e, p
}

// layered plans n tasks in layers of `width`, each requiring every task of
// the previous layer, applied in batches of 200 operations.
func layered(b *testing.B, e *app.Engine, pid project.ID, n, width int) {
	b.Helper()
	ctx := context.Background()
	var ops []plan.Change
	for i := 0; i < n; i++ {
		t := plan.AddTask{Key: fmt.Sprintf("t%d", i), Title: fmt.Sprintf("task %d", i), TaskChecks: []verification.CheckSpec{{ID: "u", Command: []string{"true"}, Required: true}}}
		if i >= width {
			start := (i/width - 1) * width
			for j := start; j < start+width; j++ {
				t.Requires = append(t.Requires, plan.Ref(fmt.Sprintf("t%d", j)))
			}
		}
		ops = append(ops, t)
		if len(ops) == 200 || i == n-1 {
			if _, err := e.Apply(ctx, plan.ChangeSet{ProjectID: pid, Operations: ops}); err != nil {
				b.Fatal(err)
			}
			ops = nil
		}
	}
}

func BenchmarkApply200Tasks(b *testing.B) {
	e, p := benchEngine(b)
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		pid := p.ID
		b.StartTimer()
		var ops []plan.Change
		for j := 0; j < 200; j++ {
			t := plan.AddTask{Title: "t", TaskChecks: []verification.CheckSpec{{ID: "u", Command: []string{"true"}, Required: true}}}
			ops = append(ops, t)
		}
		if _, err := e.Apply(context.Background(), plan.ChangeSet{ProjectID: pid, Operations: ops}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkListReady(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(fmt.Sprintf("tasks=%d", n), func(b *testing.B) {
			e, p := benchEngine(b)
			layered(b, e, p.ID, n, 10)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				snap, err := e.List(context.Background(), app.ListQuery{ProjectID: p.ID, Filter: app.FilterReady})
				if err != nil || len(snap.Tasks) != 10 {
					b.Fatalf("%d %v", len(snap.Tasks), err)
				}
			}
		})
	}
}

func BenchmarkClaim(b *testing.B) {
	e, p := benchEngine(b)
	layered(b, e, p.ID, b.N+10, 1000000) // no edges: every task ready
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Claim(context.Background(), app.ClaimRequest{ProjectID: p.ID}); err != nil {
			b.Fatal(err)
		}
	}
}
