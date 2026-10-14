package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

func init() {
	register("init", "register the current directory (or --path) as a Git project (git init if needed) and install the agent instructions: AGENTS.md block + CLAUDE.md import; re-run to refresh", runInit)
	register("projects", "list registered projects", runProjects)
	register("project", "show or configure the current project (trusted): --target-branch, --attempts", runProject)
	register("add", "plan a task or group: atask add \"title\" [--key K] [--group] [--parent REF] [--requires REF]* [--blocks REF]* --proof \"run: cmd\" --regression \"run: cmd\" | atask add -f plan.yaml|plan.json|-", runAdd)
	register("update", "change a plan item: atask update REF [--requires REF]* [--remove-requires REF]* [--proof ..] [--regression ..] [--archive --reason ..] [--reset-attempts]", runUpdate)
	register("show", "show a task or group: atask show [REF] [--full] (no REF: the current claim)", runShow)
	register("list", "list non-archived tasks: atask [--project NAME] list [ready|blocked|complete|archived|open]; ◌ marks a draft, △ a task whose checks changed after it was first claimed", runList)
	register("status", "board-wide counts, or one project with --project", runStatus)
	register("prune", "remove worktrees of complete/archived tasks and quarantined directories of idle tasks", runPrune)
}

// stringList collects a repeatable flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// changeSet builds the ChangeSet; session authority comes from
// ATASK_SESSION or the worktree the command runs in (same sources as the
// execution verbs, so `add --blocks` works from inside the worktree).
func (c *ctxt) changeSet(ctx context.Context, pid project.ID, ops ...plan.Change) plan.ChangeSet {
	session := c.env.Getenv("ATASK_SESSION")
	if session == "" {
		session = workspace.LoadToken(ctx, c.env.Cwd)
	}
	return plan.ChangeSet{ProjectID: pid, Session: session, Operations: ops}
}

func runInit(ctx context.Context, c *ctxt, args []string) error {
	name := c.fs.String("name", "", "display name (default: directory name)")
	path := c.fs.String("path", "", "directory to register (default: current directory)")
	if err := c.parse(args); err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	dir := *path
	if dir == "" {
		dir = c.env.Cwd
	}
	res, err := e.InitProjectResult(ctx, *name, dir)
	if err != nil {
		return err
	}
	p := res.Project
	type out struct {
		app.InitResult
		Instructions *app.InstallResult `json:"instructions,omitempty"`
	}
	o := out{InitResult: res}
	ir, err := app.InstallInstructions(p.RootPath)
	if err != nil {
		return err
	}
	o.Instructions = &ir
	return c.emit(o, func(w io.Writer) {
		if res.Existing {
			say(w, "✓ Project %s · %s (already registered)\n", p.ID, p.Name)
		} else {
			say(w, "✓ Registered project %s · %s\n", p.ID, p.Name)
		}
		say(w, "Root:        %s\n", p.RootPath)
		if res.CreatedRepo {
			sayln(w, "Repository:  created (git init + initial commit)")
		}
		if res.Warning != "" {
			say(w, "Warning:     %s\n", res.Warning)
			for _, f := range res.UntrackedFiles {
				say(w, "             untracked: %s\n", f)
			}
		}
		say(w, "Target:      %s (task branches atask/<id>)\n", p.TargetBranch)
		say(w, "State:       %s\n", e.Path())
		if ir := o.Instructions; ir != nil {
			switch {
			case ir.Created:
				say(w, "Agents:      wrote %s (edit the guidelines below the at block) and CLAUDE.md (@AGENTS.md)\n", ir.AgentsMD)
			case ir.Updated:
				say(w, "Agents:      refreshed the at block in %s\n", ir.AgentsMD)
			default:
				say(w, "Agents:      %s is current\n", ir.AgentsMD)
			}
		}
	})
}

func runProjects(ctx context.Context, c *ctxt, args []string) error {
	if err := c.parse(args); err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	ps, err := e.Projects(ctx)
	if err != nil {
		return err
	}
	if ps == nil {
		ps = []project.Project{}
	}
	return c.emit(ps, func(w io.Writer) { _ = renderProjects(w, ps) })
}

// ProjectView is what `atask project` reports: identity, location, target
// branch and attempt budget. A project holds nothing else.
type ProjectView struct {
	ID           project.ID `json:"id"`
	Name         string     `json:"name"`
	Path         string     `json:"path"`
	TargetBranch string     `json:"target_branch"`
	Attempts     int        `json:"attempts"`
}

func runProject(ctx context.Context, c *ctxt, args []string) error {
	target := c.fs.String("target-branch", "", "branch verified work is promoted to")
	attempts := c.fs.Int("attempts", -1, "failed attempts before a task needs attention (0 = unlimited)")
	if err := c.parse(args); err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	pid, err := c.resolveProject(ctx)
	if err != nil {
		return err
	}
	p, err := e.Project(ctx, pid)
	if err != nil {
		return err
	}
	changed := false
	if *target != "" {
		p.TargetBranch, changed = *target, true
	}
	if *attempts >= 0 {
		p.MaxAttempts, changed = *attempts, true
	}
	if changed {
		if err := e.UpdateProject(ctx, p); err != nil {
			return err
		}
		if p, err = e.Project(ctx, pid); err != nil {
			return err
		}
	}
	v := ProjectView{ID: p.ID, Name: p.Name, Path: p.RootPath, TargetBranch: p.TargetBranch, Attempts: p.MaxAttempts}
	return c.emit(v, func(w io.Writer) {
		say(w, "Project:  %s · %s\nPath:     %s\nTarget:   %s\nAttempts: %d\n", v.ID, v.Name, v.Path, v.TargetBranch, v.Attempts)
	})
}

func refs(raw stringList) []plan.Ref {
	var out []plan.Ref
	for _, r := range raw {
		out = append(out, plan.Ref(strings.TrimSpace(r)))
	}
	return out
}

func runAdd(ctx context.Context, c *ctxt, args []string) error {
	key := c.fs.String("key", "", "stable project-local key")
	group := c.fs.Bool("group", false, "create an organizational group instead of a task")
	parent := c.fs.String("parent", "", "enclosing group (id or key)")
	outcome := c.fs.String("outcome", "", "the single intended outcome (default: the title)")
	var constraints, accept, requires, blocks stringList
	c.fs.Var(&constraints, "constraint", "constraint (repeatable)")
	c.fs.Var(&accept, "accept", "acceptance criterion (repeatable)")
	pfl := c.bindProofFlags()
	c.fs.Var(&requires, "requires", "hard prerequisite (id or key, repeatable)")
	c.fs.Var(&blocks, "blocks", "existing task that will require the new one (repeatable)")
	file := c.fs.String("f", "", "plan file (JSON or YAML; '-' for stdin): one item, a list, or {tasks: [...]}; applied atomically")
	if err := c.parse(args); err != nil {
		return err
	}
	if *file != "" {
		if len(c.args) != 0 || *key != "" || *group || *outcome != "" || pfl.proof != "" || pfl.regression != "" || len(requires)+len(blocks) > 0 {
			return usage("-f takes the whole plan; no title or task flags alongside")
		}
		return c.addFromPlan(ctx, *file)
	}
	if len(c.args) != 1 {
		return usage("add needs exactly one title argument (or -f FILE)")
	}
	pid, err := c.resolveProject(ctx)
	if err != nil {
		return err
	}
	var op plan.Change
	if *group {
		if len(requires)+len(blocks)+len(accept)+len(constraints) > 0 || *outcome != "" {
			return usage("--group takes --key, --parent, --proof and --regression (run on the merge of every child's completed revision once every child is complete); prerequisites, blockers, outcome and acceptance belong to tasks")
		}
		g := plan.AddGroup{Key: *key, Title: c.args[0], Parent: plan.Ref(*parent)}
		if pfl.proof != "" || pfl.regression != "" {
			if err := pfl.requireBoth(); err != nil {
				return err
			}
			if g.Proof, g.Regression, err = pfl.parse(); err != nil {
				return err
			}
		}
		op = g
	} else {
		t := plan.AddTask{Key: *key, Title: c.args[0], Outcome: *outcome, Parent: plan.Ref(*parent), Constraints: constraints, Acceptance: accept, Requires: refs(requires), Blocks: refs(blocks)}
		if t.Proof, t.Regression, err = pfl.parse(); err != nil {
			return err
		}
		if t.OnComplete, err = pfl.parseActions(); err != nil {
			return err
		}
		op = t
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	res, err := e.Apply(ctx, c.changeSet(ctx, pid, op))
	if err != nil {
		return err
	}
	var id task.ID
	for _, v := range res.Created {
		id = v
	}
	if id == "" {
		// Identical replay: look the key up for the acknowledgement.
		if *key != "" {
			if v, err := e.Show(ctx, pid, *key, false); err == nil {
				id = v.ID
			}
		}
	}
	return c.emitAdd(ctx, pid, res, id, c.args[0])
}

// addOut is the acknowledgement of `atask add`: the plan result and the task
// (single-item adds).
type addOut struct {
	plan.Result
	Task *app.TaskView `json:"task,omitempty"`
}

func (c *ctxt) emitAdd(ctx context.Context, pid project.ID, res plan.Result, id task.ID, title string) error {
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	o := addOut{Result: res}
	if id != "" {
		if view, err := e.Show(ctx, pid, string(id), false); err == nil {
			o.Task = &view
		}
	}
	return c.emit(o, func(w io.Writer) {
		verb := "Created"
		if res.Replayed || res.Changed == 0 {
			verb = "Unchanged"
		}
		if id != "" {
			say(w, "✓ %s %s · %s\n", verb, id, title)
		} else {
			say(w, "✓ %s %d item(s)\n", verb, len(res.Created))
			for name, cid := range res.Created {
				say(w, "  %s  %s\n", cid, name)
			}
		}
		if o.Task != nil {
			renderAck(w, *o.Task)
		}
		renderWarnings(w, res.Warnings)
		say(w, "Plan rev: %d\n", res.PlanRev)
	})
}

// addFromPlan applies a plan file or inline JSON as one atomic revision.
func (c *ctxt) addFromPlan(ctx context.Context, file string) error {
	name, r, err := openPlanFile(file, c.env.Stdin)
	if err != nil {
		return err
	}
	defer discard.CloseReadOnly(r)
	items, err := readPlanItems(name, r)
	if err != nil {
		return err
	}
	pid, err := c.resolveProject(ctx)
	if err != nil {
		return err
	}
	ops := make([]plan.Change, 0, len(items))
	for i := range items {
		op, err := items[i].change(i)
		if err != nil {
			return err
		}
		ops = append(ops, op)
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	res, err := e.Apply(ctx, c.changeSet(ctx, pid, ops...))
	if err != nil {
		return err
	}
	var id task.ID
	title := ""
	if len(items) == 1 {
		title = items[0].Title
		for _, v := range res.Created {
			id = v
		}
		if id == "" && items[0].Key != "" {
			if v, err := e.Show(ctx, pid, items[0].Key, false); err == nil {
				id = v.ID
			}
		}
	}
	return c.emitAdd(ctx, pid, res, id, title)
}

func runUpdate(ctx context.Context, c *ctxt, args []string) error {
	outcome := c.fs.String("outcome", "", "new outcome")
	parent := c.fs.String("parent", "\x00", "move into a group (id or key); empty string clears")
	var constraints, accept, requires, remove stringList
	c.fs.Var(&constraints, "constraint", "replace constraints (repeatable)")
	c.fs.Var(&accept, "accept", "replace acceptance criteria (repeatable)")
	pfl := c.bindProofFlags()
	c.fs.Var(&requires, "requires", "add a hard prerequisite (repeatable)")
	c.fs.Var(&remove, "remove-requires", "remove a hard prerequisite (repeatable)")
	archive := c.fs.Bool("archive", false, "archive (soft-delete) the task or group; needs --reason")
	reason := c.fs.String("reason", "", "why the item is archived (recorded; required with --archive)")
	reset := c.fs.Bool("reset-attempts", false, "clear failure bookkeeping so the task can be claimed again")
	if err := c.parse(args); err != nil {
		return err
	}
	if len(c.args) != 1 {
		return usage("update needs exactly one task reference")
	}
	pid, err := c.resolveProject(ctx)
	if err != nil {
		return err
	}
	target := plan.Ref(c.args[0])
	var ops []plan.Change
	if *archive {
		if strings.TrimSpace(*reason) == "" {
			return usage("--archive needs --reason: say why the item is leaving the plan")
		}
		if *outcome != "" || *parent != "\x00" || len(constraints)+len(accept)+len(requires)+len(remove) > 0 || *reset || pfl.proof != "" || pfl.regression != "" || len(pfl.actions) > 0 {
			return usage("--archive cannot be combined with other edits; archive in its own update")
		}
		ops = append(ops, plan.ArchiveTask{Target: target, Reason: *reason})
	} else {
		u := plan.UpdateTask{Target: target, ResetAttempts: *reset}
		if *outcome != "" {
			u.Outcome = outcome
		}
		if *parent != "\x00" {
			p := plan.Ref(*parent)
			u.Parent = &p
		}
		if len(constraints) > 0 {
			u.Constraints = plan.Replace([]string(constraints))
		}
		if len(accept) > 0 {
			u.Acceptance = plan.Replace([]string(accept))
		}
		if pfl.proof != "" || pfl.regression != "" {
			if u.Proof, u.Regression, err = pfl.parse(); err != nil {
				return err
			}
		}
		if len(pfl.actions) > 0 {
			acts, err := pfl.parseActions()
			if err != nil {
				return err
			}
			u.OnComplete = plan.Replace(acts)
		}
		u.AddRequires = refs(requires)
		u.RemoveRequires = refs(remove)
		ops = append(ops, u)
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	res, err := e.Apply(ctx, c.changeSet(ctx, pid, ops...))
	if err != nil {
		return err
	}
	view, verr := e.Show(ctx, pid, string(target), false)
	type out struct {
		plan.Result
		Task *app.TaskView `json:"task,omitempty"`
	}
	o := out{Result: res}
	if verr == nil {
		o.Task = &view
	}
	return c.emit(o, func(w io.Writer) {
		verb := "Updated"
		if *archive {
			verb = "Archived"
		}
		if res.Changed == 0 {
			verb = "Unchanged"
		}
		if verr == nil {
			say(w, "✓ %s %s · %s\n", verb, view.ID, view.Title)
			renderAck(w, view)
		} else {
			say(w, "✓ %s %s\n", verb, target)
		}
		renderWarnings(w, res.Warnings)
		say(w, "Plan rev: %d\n", res.PlanRev)
	})
}

func runShow(ctx context.Context, c *ctxt, args []string) error {
	full := c.fs.Bool("full", false, "include full history, runs and evidence")
	if err := c.parse(args); err != nil {
		return err
	}
	if len(c.args) == 0 {
		return c.showCurrent(ctx, *full)
	}
	if len(c.args) != 1 {
		return usage("show takes a task id, or none to show the current claim")
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	pid := project.ID("")
	if !task.IsID(c.args[0]) {
		if pid, err = c.resolveProject(ctx); err != nil {
			return err
		}
	}
	v, err := e.Show(ctx, pid, c.args[0], *full)
	if err != nil {
		return err
	}
	p := c.painter()
	return c.emit(v, func(w io.Writer) { renderShow(w, v, *full, p) })
}

func (c *ctxt) showCurrent(ctx context.Context, full bool) error {
	raw := c.env.Getenv("ATASK_SESSION")
	if strings.TrimSpace(raw) == "" {
		raw = workspace.LoadToken(ctx, c.env.Cwd)
	}
	if strings.TrimSpace(raw) == "" {
		return usage("a task ID is required outside an active claim")
	}
	tok, err := execution.ParseToken(raw)
	if err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	v, err := e.Whoami(ctx, tok)
	if err != nil {
		return err
	}
	p := c.painter()
	return c.emit(v, func(w io.Writer) { renderShow(w, v, full, p) })
}

func renderProjects(w io.Writer, ps []project.Project) error {
	if len(ps) == 0 {
		_, err := fmt.Fprintln(w, "no projects registered. run `atask init` here.")
		return err
	}
	for i := range ps {
		if _, err := fmt.Fprintf(w, "%s  %-20s  %s\n", ps[i].ID, ps[i].Name, ps[i].RootPath); err != nil {
			return err
		}
	}
	return nil
}

func listFilter(s string) (app.Filter, bool) {
	switch s {
	case "ready", "blocked", "draft", "complete", "archived", "open", "all":
		return app.Filter(s), true
	}
	return "", false
}

func runList(ctx context.Context, c *ctxt, args []string) error {
	if err := c.parse(args); err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	filter := app.FilterAll
	switch len(c.args) {
	case 0:
	case 1:
		f, ok := listFilter(c.args[0])
		if !ok {
			return usage("list takes only a filter (ready, blocked, complete, archived, open); select a project with --project NAME, not %q", c.args[0])
		}
		filter = f
	default:
		return usage("list takes at most one filter (ready, blocked, complete, archived, open); select a project with --project NAME")
	}
	var pid project.ID
	if strings.TrimSpace(c.g.project) != "" {
		p, err := e.ResolveProject(ctx, c.g.project, "")
		if err != nil {
			return err
		}
		pid = p.ID
	}
	snap, err := e.List(ctx, app.ListQuery{ProjectID: pid, Filter: filter})
	if err != nil {
		return err
	}
	p := c.painter()
	return c.emit(snap, func(w io.Writer) {
		if len(snap.Tasks) == 0 && len(snap.Groups) == 0 {
			if pid == "" {
				_, _ = fmt.Fprintln(w, "no tasks registered.")
				return
			}
			name := string(pid)
			if proj, err := e.Project(ctx, pid); err == nil && proj.Name != "" {
				name = proj.Name
			}
			_, _ = fmt.Fprintf(w, "no tasks registered in %s.\n", name)
			return
		}
		renderList(w, snap, filter, p)
	})
}

func runStatus(ctx context.Context, c *ctxt, args []string) error {
	if err := c.parse(args); err != nil {
		return err
	}
	var pid project.ID
	if strings.TrimSpace(c.g.project) != "" {
		id, err := c.resolveProject(ctx)
		if err != nil {
			return err
		}
		pid = id
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	s, err := e.Summary(ctx, pid)
	if err != nil {
		return err
	}
	return c.emit(s, func(w io.Writer) { renderSummary(w, s) })
}

var _ = time.Second

func runPrune(ctx context.Context, c *ctxt, args []string) error {
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
	res, err := e.PruneWorkspaces(ctx, pid)
	if err != nil {
		return err
	}
	return c.emit(res, func(w io.Writer) {
		say(w, "✓ Pruned %d director%s\n", len(res.Removed), map[bool]string{true: "y", false: "ies"}[len(res.Removed) == 1])
		for _, p := range res.Removed {
			say(w, "  - %s\n", p)
		}
	})
}
