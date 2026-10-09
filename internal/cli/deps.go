package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/dependency"
	"github.com/zachbornheimer/ai-task/internal/task"
)

func init() {
	register("deps", "manage dependencies: deps add B --requires A | deps remove B --requires A | deps list B", runDeps)
	register("graph", "print the dependency graph: --format text|json|dot|edges [--around TASK --depth N]", runGraph)
}

func runDeps(ctx context.Context, c *ctxt, args []string) error {
	requires := c.fs.String("requires", "", "the prerequisite task (B --requires A: B requires A)")
	if err := c.parse(args); err != nil {
		return err
	}
	if len(c.args) < 1 {
		return usage("deps needs a subcommand: add, remove, list")
	}
	sub, rest := c.args[0], c.args[1:]
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	switch sub {
	case "add", "remove":
		if len(rest) != 1 || *requires == "" {
			return usage("deps %s <task> --requires <prerequisite>", sub)
		}
		t, err := task.ParseID(rest[0])
		if err != nil {
			return err
		}
		r, err := task.ParseID(*requires)
		if err != nil {
			return err
		}
		edge := dependency.Edge{Task: t, Requires: r}
		if sub == "add" {
			err = e.AddDependency(ctx, t, r)
		} else {
			err = e.RemoveDependency(ctx, t, r)
		}
		if err != nil {
			return err
		}
		verb := "now requires"
		if sub == "remove" {
			verb = "no longer requires"
		}
		return c.emit(edge, func(w io.Writer) { fmt.Fprintf(w, "%s %s %s\n", t, verb, r) })
	case "list":
		if len(rest) != 1 {
			return usage("deps list <task>")
		}
		t, err := task.ParseID(rest[0])
		if err != nil {
			return err
		}
		requires, requiredBy, err := e.Dependencies(ctx, t)
		if err != nil {
			return err
		}
		type out struct {
			Task       task.ID           `json:"task"`
			Requires   []app.TaskSummary `json:"requires"`
			RequiredBy []app.TaskSummary `json:"required_by"`
		}
		if requires == nil {
			requires = []app.TaskSummary{}
		}
		if requiredBy == nil {
			requiredBy = []app.TaskSummary{}
		}
		return c.emit(out{t, requires, requiredBy}, func(w io.Writer) {
			fmt.Fprintf(w, "%s requires:\n", t)
			for _, d := range requires {
				fmt.Fprintf(w, "  %s  [%s] %s\n", d.ID, d.Status, d.Description)
			}
			fmt.Fprintf(w, "%s is required by:\n", t)
			for _, d := range requiredBy {
				fmt.Fprintf(w, "  %s  [%s] %s\n", d.ID, d.Status, d.Description)
			}
		})
	}
	return usage("unknown deps subcommand %q", sub)
}

func runGraph(ctx context.Context, c *ctxt, args []string) error {
	format := c.fs.String("format", "text", "text|json|dot|edges")
	around := c.fs.String("around", "", "restrict to the neighbourhood of a task")
	depth := c.fs.Int("depth", 1, "neighbourhood depth for --around")
	if err := c.parse(args); err != nil {
		return err
	}
	pid, err := c.resolveProject(ctx)
	if err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	g, err := e.Graph(ctx, pid)
	if err != nil {
		return err
	}
	if *around != "" {
		id, err := task.ParseID(*around)
		if err != nil {
			return err
		}
		g = g.Around(id, *depth)
	}
	if g.Nodes == nil {
		g.Nodes = []dependency.Node{}
	}
	if g.Edges == nil {
		g.Edges = []dependency.Edge{}
	}
	switch *format {
	case "json":
		c.g.json = true
		return c.emit(g, nil)
	case "dot":
		return c.emit(g, func(w io.Writer) { renderDot(w, g) })
	case "edges":
		return c.emit(g.Edges, func(w io.Writer) {
			for _, ed := range g.Edges {
				fmt.Fprintf(w, "%s requires %s\n", ed.Task, ed.Requires)
			}
		})
	case "text":
		return c.emit(g, func(w io.Writer) { renderGraphText(w, g) })
	}
	return usage("unknown --format %q", *format)
}

func renderGraphText(w io.Writer, g dependency.Graph) {
	order, err := g.TopologicalOrder()
	if err != nil {
		fmt.Fprintf(w, "error: %v\n", err)
		return
	}
	byID := map[task.ID]dependency.Node{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	reqs := map[task.ID][]task.ID{}
	for _, e := range g.Edges {
		reqs[e.Task] = append(reqs[e.Task], e.Requires)
	}
	for _, id := range order {
		n := byID[id]
		fmt.Fprintf(w, "%s  [%s] %s\n", n.ID, n.Status, n.Description)
		for _, r := range reqs[id] {
			fmt.Fprintf(w, "    requires %s [%s]\n", r, byID[r].Status)
		}
	}
}

func renderDot(w io.Writer, g dependency.Graph) {
	fmt.Fprintln(w, "digraph tasks {")
	fmt.Fprintln(w, "  rankdir=BT;")
	fmt.Fprintln(w, "  node [shape=box];")
	for _, n := range g.Nodes {
		label := strings.ReplaceAll(n.Description, `"`, `\"`)
		if len(label) > 40 {
			label = label[:37] + "..."
		}
		fmt.Fprintf(w, "  %q [label=\"%s\\n%s\\n[%s]\"];\n", n.ID, n.ID, label, n.Status)
	}
	for _, e := range g.Edges {
		// An arrow from the dependent to its prerequisite reads "requires".
		fmt.Fprintf(w, "  %q -> %q;\n", e.Task, e.Requires)
	}
	fmt.Fprintln(w, "}")
}
