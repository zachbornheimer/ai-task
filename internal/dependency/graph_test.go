package dependency

import (
	"context"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// memChecker is a tiny in-memory adjacency used only to exercise Validate's
// rule ordering; the real Checker is the SQLite store.
type memChecker struct{ edges map[Edge]bool }

func (m memChecker) Exists(_ context.Context, e Edge) (bool, error) { return m.edges[e], nil }
func (m memChecker) Reaches(_ context.Context, from, to task.ID) (bool, error) {
	seen := map[task.ID]bool{}
	var walk func(task.ID) bool
	walk = func(cur task.ID) bool {
		if cur == to {
			return true
		}
		if seen[cur] {
			return false
		}
		seen[cur] = true
		for e := range m.edges {
			if e.Task == cur && walk(e.Requires) {
				return true
			}
		}
		return false
	}
	return walk(from), nil
}

func TestValidate(t *testing.T) {
	a, b, c := task.ID("task-aaaaaaaaaaaaaaaa"), task.ID("task-bbbbbbbbbbbbbbbb"), task.ID("task-cccccccccccccccc")
	ctx := context.Background()
	m := memChecker{edges: map[Edge]bool{{Task: b, Requires: a}: true, {Task: c, Requires: b}: true}}
	cases := []struct {
		name string
		e    Edge
		same bool
		code fault.Code
	}{
		{"self", Edge{a, a}, true, fault.CodeSelfDependency},
		{"cross project", Edge{a, c}, false, fault.CodeCrossProject},
		{"duplicate", Edge{b, a}, true, fault.CodeDuplicateDependency},
		{"direct cycle", Edge{a, b}, true, fault.CodeDependencyCycle},
		{"transitive cycle a->b->c->a", Edge{a, c}, true, fault.CodeDependencyCycle},
		{"ok diamond", Edge{c, a}, true, ""},
	}
	for _, tc := range cases {
		err := Validate(ctx, tc.e, tc.same, m)
		if fault.CodeOf(err) != tc.code {
			t.Errorf("%s: got %v want %q", tc.name, err, tc.code)
		}
	}
}

func TestUnmet(t *testing.T) {
	got := Unmet([]Prerequisite{{"task-1", true}, {"task-2", false}, {"task-3", false}})
	if len(got) != 2 || got[0] != "task-2" {
		t.Fatalf("unmet = %v", got)
	}
	if Unmet(nil) != nil {
		t.Fatal("nil in, nil out")
	}
}

func TestTopologicalOrderAndAround(t *testing.T) {
	a, b, c, d := task.ID("task-a"), task.ID("task-b"), task.ID("task-c"), task.ID("task-d")
	g := Graph{
		Nodes: []Node{{ID: d}, {ID: c}, {ID: b}, {ID: a}},
		Edges: []Edge{{Task: b, Requires: a}, {Task: c, Requires: b}, {Task: d, Requires: a}},
	}
	order, err := g.TopologicalOrder()
	if err != nil {
		t.Fatal(err)
	}
	pos := map[task.ID]int{}
	for i, id := range order {
		pos[id] = i
	}
	for _, e := range g.Edges {
		if pos[e.Requires] > pos[e.Task] {
			t.Fatalf("order violates %v: %v", e, order)
		}
	}
	// Deterministic: a first, then d (listed before b in Nodes), then b, c.
	if order[0] != a || order[1] != d || order[2] != b || order[3] != c {
		t.Fatalf("unexpected order %v", order)
	}
	cyc := Graph{Nodes: []Node{{ID: a}, {ID: b}}, Edges: []Edge{{a, b}, {b, a}}}
	if _, err := cyc.TopologicalOrder(); !fault.Is(err, fault.CodeDependencyCycle) {
		t.Fatal("cycle not reported")
	}
	around := g.Around(b, 1)
	if len(around.Nodes) != 3 || len(around.Edges) != 2 {
		t.Fatalf("around depth1: %+v", around)
	}
	around2 := g.Around(b, 2)
	if len(around2.Nodes) != 4 {
		t.Fatalf("around depth2: %+v", around2)
	}
	if n := len(g.Around(b, 0).Nodes); n != 1 {
		t.Fatalf("around depth0 = %d nodes", n)
	}
}
