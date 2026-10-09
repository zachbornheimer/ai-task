package app_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/dependency"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// benchEngine opens a file-backed engine (the realistic case; WAL on disk).
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

// layeredDAG builds n tasks in layers of `width`, each task requiring every
// task in the previous layer: a dense, realistic shape (feature → subtasks →
// integration) that makes availability queries do real work.
func layeredDAG(b *testing.B, e *app.Engine, pid project.ID, n, width int) []task.ID {
	b.Helper()
	ctx := context.Background()
	ids := make([]task.ID, 0, n)
	for i := 0; i < n; i++ {
		t, _, err := e.Add(ctx, task.Spec{ProjectID: pid, Description: fmt.Sprintf("task %d", i)})
		if err != nil {
			b.Fatal(err)
		}
		ids = append(ids, t.ID)
	}
	var edges []dependency.Edge
	for i := width; i < n; i++ {
		layerStart := (i/width - 1) * width
		for j := layerStart; j < layerStart+width; j++ {
			edges = append(edges, dependency.Edge{Task: ids[i], Requires: ids[j]})
		}
	}
	for start := 0; start < len(edges); start += 500 {
		end := start + 500
		if end > len(edges) {
			end = len(edges)
		}
		if err := e.AddDependencies(ctx, edges[start:end]); err != nil {
			b.Fatal(err)
		}
	}
	return ids
}

func BenchmarkAvailable(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("tasks=%d", n), func(b *testing.B) {
			e, p := benchEngine(b)
			layeredDAG(b, e, p.ID, n, 10)
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err := e.Available(ctx, p.ID)
				if err != nil || len(out) != 10 {
					b.Fatalf("%d %v", len(out), err)
				}
			}
		})
	}
}

func BenchmarkListAll(b *testing.B) {
	for _, n := range []int{1000, 5000} {
		b.Run(fmt.Sprintf("tasks=%d", n), func(b *testing.B) {
			e, p := benchEngine(b)
			layeredDAG(b, e, p.ID, n, 10)
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := e.List(ctx, p.ID, app.ListFilter{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkShow(b *testing.B) {
	e, p := benchEngine(b)
	ids := layeredDAG(b, e, p.ID, 1000, 10)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Show(ctx, ids[500]); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGraph(b *testing.B) {
	for _, n := range []int{1000, 5000} {
		b.Run(fmt.Sprintf("tasks=%d", n), func(b *testing.B) {
			e, p := benchEngine(b)
			layeredDAG(b, e, p.ID, n, 10)
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				g, err := e.Graph(ctx, p.ID)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := g.TopologicalOrder(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkTakeAuto measures claiming the next available task from a large
// project; each iteration consumes one task.
func BenchmarkTakeAuto(b *testing.B) {
	e, p := benchEngine(b)
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		if _, _, err := e.Add(ctx, task.Spec{ProjectID: p.ID, Description: "t"}); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Take(ctx, app.TakeRequest{Project: p.ID}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkTakeContention runs parallel claims on one task; all but the
// first fail fast with TASK_ALREADY_TAKEN. It measures the cost of a
// contended immediate transaction.
func BenchmarkTakeContention(b *testing.B) {
	e, p := benchEngine(b)
	ctx := context.Background()
	t, _, _ := e.Add(ctx, task.Spec{ProjectID: p.ID, Description: "hot"})
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := e.Take(ctx, app.TakeRequest{Task: &t.ID})
			if err != nil && !fault.Is(err, fault.CodeTaskAlreadyTaken) {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkLogParallel(b *testing.B) {
	e, p := benchEngine(b)
	ctx := context.Background()
	tokens := make(chan execution.Token, 64)
	for i := 0; i < 64; i++ {
		t, _, _ := e.Add(ctx, task.Spec{ProjectID: p.ID, Description: "t"})
		s, err := e.Take(ctx, app.TakeRequest{Task: &t.ID, Lease: 24 * time.Hour})
		if err != nil {
			b.Fatal(err)
		}
		tokens <- s.Token
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		tok := <-tokens
		defer func() { tokens <- tok }()
		for pb.Next() {
			if _, err := e.Log(ctx, tok, execution.LogEntry{Done: "step"}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkAddDependencyChain measures inserting an edge whose cycle check
// must walk a chain of the given length (the worst case for the CTE).
func BenchmarkAddDependencyChain(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("chain=%d", n), func(b *testing.B) {
			e, p := benchEngine(b)
			ctx := context.Background()
			ids := make([]task.ID, n)
			for i := range ids {
				t, _, _ := e.Add(ctx, task.Spec{ProjectID: p.ID, Description: "c"})
				ids[i] = t.ID
			}
			var edges []dependency.Edge
			for i := 1; i < n; i++ {
				edges = append(edges, dependency.Edge{Task: ids[i], Requires: ids[i-1]})
			}
			if err := e.AddDependencies(ctx, edges); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Adding "head requires tail" must traverse the whole chain
				// (tail requires ... head) to discover the cycle.
				err := e.AddDependency(ctx, ids[0], ids[n-1])
				if !fault.Is(err, fault.CodeDependencyCycle) {
					b.Fatalf("expected cycle, got %v", err)
				}
			}
		})
	}
}

func BenchmarkAddDependencyBulk(b *testing.B) {
	e, p := benchEngine(b)
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		ids := make([]task.ID, 200)
		for j := range ids {
			t, _, _ := e.Add(ctx, task.Spec{ProjectID: p.ID, Description: "b"})
			ids[j] = t.ID
		}
		var edges []dependency.Edge
		for j := 10; j < 200; j++ {
			for k := j - 10; k < j; k++ {
				edges = append(edges, dependency.Edge{Task: ids[j], Requires: ids[k]})
			}
		}
		b.StartTimer()
		if err := e.AddDependencies(ctx, edges); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAdd(b *testing.B) {
	e, p := benchEngine(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := e.Add(ctx, task.Spec{ProjectID: p.ID, Description: "t"}); err != nil {
			b.Fatal(err)
		}
	}
}
