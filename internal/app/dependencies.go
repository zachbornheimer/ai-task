package app

import (
	"context"
	"time"

	"github.com/zachbornheimer/ai-task/internal/dependency"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// AddDependency records "t requires r". Validation and insertion share one
// immediate transaction, so concurrent writers cannot close a cycle.
func (e *Engine) AddDependency(ctx context.Context, t, r task.ID) error {
	return e.AddDependencies(ctx, []dependency.Edge{{Task: t, Requires: r}})
}

// AddDependencies adds several edges atomically: either every edge is valid
// against the graph as it stands after the previous edges, or none is added.
func (e *Engine) AddDependencies(ctx context.Context, edges []dependency.Edge) error {
	if len(edges) == 0 {
		return nil
	}
	now := e.now()
	return e.store.Write(ctx, func(tx *sqlite.Tx) error {
		return e.insertEdges(ctx, tx, edges, now)
	})
}

// insertEdges validates and inserts edges inside an open write transaction.
func (e *Engine) insertEdges(ctx context.Context, tx *sqlite.Tx, edges []dependency.Edge, now time.Time) error {
	for _, edge := range edges {
		pa, err := tx.TaskProject(edge.Task)
		if err != nil {
			return err
		}
		pb := pa
		if edge.Requires != edge.Task {
			if pb, err = tx.TaskProject(edge.Requires); err != nil {
				return err
			}
		}
		if err := dependency.Validate(ctx, edge, pa == pb, tx); err != nil {
			return err
		}
		if err := tx.InsertEdge(edge, now); err != nil {
			return err
		}
	}
	return nil
}

// RemoveDependency deletes "t requires r". Eligibility is derived, so no
// recalculation is needed: the next read sees the new graph.
func (e *Engine) RemoveDependency(ctx context.Context, t, r task.ID) error {
	return e.store.Write(ctx, func(tx *sqlite.Tx) error {
		ok, err := tx.DeleteEdge(dependency.Edge{Task: t, Requires: r})
		if err != nil {
			return err
		}
		if !ok {
			return fault.New(fault.CodeNotFound, "%s does not require %s", t, r)
		}
		return nil
	})
}

// Dependencies returns what a task requires and what requires it.
func (e *Engine) Dependencies(ctx context.Context, id task.ID) (requires, requiredBy []TaskSummary, err error) {
	now := e.now()
	err = e.store.Read(ctx, func(tx *sqlite.Tx) error {
		if _, err := tx.GetRecord(id); err != nil {
			return err
		}
		reqs, err := tx.Requirements(id)
		if err != nil {
			return err
		}
		for _, r := range reqs {
			requires = append(requires, summarize(r, now))
		}
		deps, err := tx.Dependents(id)
		if err != nil {
			return err
		}
		for _, r := range deps {
			requiredBy = append(requiredBy, summarize(r, now))
		}
		return nil
	})
	return requires, requiredBy, err
}

// Graph returns the whole dependency graph of a project as a read model.
func (e *Engine) Graph(ctx context.Context, pid project.ID) (dependency.Graph, error) {
	now := e.now()
	var g dependency.Graph
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		if _, err := tx.GetProject(pid); err != nil {
			return err
		}
		records, err := tx.ListRecords(pid, sqlite.ScopeAll, now)
		if err != nil {
			return err
		}
		for _, r := range records {
			g.Nodes = append(g.Nodes, dependency.Node{ID: r.Task.ID, Description: r.Task.Description, Status: r.Status(now)})
		}
		g.Edges, err = tx.ProjectEdges(pid)
		return err
	})
	return g, err
}
