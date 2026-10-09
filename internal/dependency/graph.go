// Package dependency owns the relationships between tasks: the single
// authoritative edge direction, the rules that keep the graph a DAG, and the
// eligibility rule that decides when prerequisites are satisfied.
package dependency

import (
	"context"
	"sort"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// Edge records that Task requires Requires: Task cannot be taken until
// Requires is complete. This is the only direction the system knows.
//
//	tasks deps add B --requires A   =>   Edge{Task: B, Requires: A}
type Edge struct {
	Task     task.ID `json:"task"`
	Requires task.ID `json:"requires"`
}

// Checker answers the two questions edge validation needs from the store.
// Both are evaluated inside the same write transaction that inserts the edge,
// so concurrent writers cannot interleave an edge that closes a cycle.
type Checker interface {
	// Exists reports whether the exact edge is already recorded.
	Exists(ctx context.Context, e Edge) (bool, error)
	// Reaches reports whether `from` transitively requires `to`.
	Reaches(ctx context.Context, from, to task.ID) (bool, error)
}

// Validate applies the DAG invariants to a proposed edge. sameProject is
// supplied by the caller because the store, not this package, knows task
// ownership. The rejection order is cheapest-first.
func Validate(ctx context.Context, e Edge, sameProject bool, c Checker) error {
	if e.Task == e.Requires {
		return fault.New(fault.CodeSelfDependency, "task %s cannot require itself", e.Task)
	}
	if !sameProject {
		return fault.New(fault.CodeCrossProject, "tasks %s and %s belong to different projects", e.Task, e.Requires)
	}
	dup, err := c.Exists(ctx, e)
	if err != nil {
		return err
	}
	if dup {
		return fault.New(fault.CodeDuplicateDependency, "%s already requires %s", e.Task, e.Requires)
	}
	// Adding "Task requires Requires" closes a cycle iff Requires already
	// (transitively) requires Task.
	cyc, err := c.Reaches(ctx, e.Requires, e.Task)
	if err != nil {
		return err
	}
	if cyc {
		return fault.New(fault.CodeDependencyCycle, "%s already requires %s (directly or transitively); adding the reverse edge would create a cycle", e.Requires, e.Task)
	}
	return nil
}

// Prerequisite is the completion fact of one required task.
type Prerequisite struct {
	ID       task.ID
	Complete bool
}

// Unmet returns the prerequisites that are not complete. Eligibility is
// simply len(Unmet(...)) == 0: in Milestone 1 "complete" already implies the
// output is usable under the project's integration policy, so no weaker
// prerequisite kind exists.
func Unmet(prereqs []Prerequisite) []task.ID {
	var out []task.ID
	for _, p := range prereqs {
		if !p.Complete {
			out = append(out, p.ID)
		}
	}
	return out
}

// Node is a task as it appears in a graph read model.
type Node struct {
	ID          task.ID     `json:"id"`
	Description string      `json:"description"`
	Status      task.Status `json:"status"`
}

// Graph is an in-memory read model of one project's dependency graph. It is
// built on demand for rendering and ordering, never used for eligibility
// decisions (those are indexed store queries).
type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// TopologicalOrder returns task IDs such that every task appears after all
// tasks it requires. Ties are broken by the order of Nodes so output is
// deterministic. A cycle (impossible for a stored graph) is reported.
func (g Graph) TopologicalOrder() ([]task.ID, error) {
	indeg := make(map[task.ID]int, len(g.Nodes))
	dependents := make(map[task.ID][]task.ID)
	index := make(map[task.ID]int, len(g.Nodes))
	for i, n := range g.Nodes {
		indeg[n.ID] = 0
		index[n.ID] = i
	}
	for _, e := range g.Edges {
		if _, ok := indeg[e.Task]; !ok {
			continue
		}
		if _, ok := indeg[e.Requires]; !ok {
			continue
		}
		indeg[e.Task]++
		dependents[e.Requires] = append(dependents[e.Requires], e.Task)
	}
	var ready []task.ID
	for _, n := range g.Nodes {
		if indeg[n.ID] == 0 {
			ready = append(ready, n.ID)
		}
	}
	byIndex := func(ids []task.ID) { sort.Slice(ids, func(i, j int) bool { return index[ids[i]] < index[ids[j]] }) }
	byIndex(ready)
	out := make([]task.ID, 0, len(g.Nodes))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		out = append(out, id)
		next := dependents[id]
		byIndex(next)
		for _, d := range next {
			indeg[d]--
			if indeg[d] == 0 {
				ready = append(ready, d)
				byIndex(ready)
			}
		}
	}
	if len(out) != len(g.Nodes) {
		return nil, fault.New(fault.CodeDependencyCycle, "graph contains a cycle")
	}
	return out, nil
}

// Around restricts the graph to the nodes within `depth` edges of center in
// either direction, keeping only edges between retained nodes.
func (g Graph) Around(center task.ID, depth int) Graph {
	keep := map[task.ID]bool{center: true}
	frontier := []task.ID{center}
	for d := 0; d < depth && len(frontier) > 0; d++ {
		var next []task.ID
		for _, id := range frontier {
			for _, e := range g.Edges {
				var other task.ID
				switch id {
				case e.Task:
					other = e.Requires
				case e.Requires:
					other = e.Task
				default:
					continue
				}
				if !keep[other] {
					keep[other] = true
					next = append(next, other)
				}
			}
		}
		frontier = next
	}
	var out Graph
	for _, n := range g.Nodes {
		if keep[n.ID] {
			out.Nodes = append(out.Nodes, n)
		}
	}
	for _, e := range g.Edges {
		if keep[e.Task] && keep[e.Requires] {
			out.Edges = append(out.Edges, e)
		}
	}
	return out
}
