package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

func init() {
	register("init", "register the current directory (or --path) as a Git project (git init if needed) and install the agent instructions: AGENTS.md block + CLAUDE.md import [--no-instructions] [--claude: Claude Code hooks]; re-run to refresh", runInit)
	register("projects", "list registered projects", runProjects)
	register("project", "show or configure the current project (trusted): --regression-json, --integration, --target-branch, --max-attempts", runProject)
	register("add", "plan a task or group: at add \"title\" [--key K] [--group] [--parent REF] [--requires REF]* [--blocks REF]* [--check ..]* [--claim] | at add -f plan.yaml|plan.json|- | at add --plan '{...}'", runAdd)
	register("update", "change a plan item: at update REF [--title ..] [--requires REF]* [--remove-requires REF]* [--check ..]* [--archive --reason ..] [--reset-attempts] [--withdraw-submission]", runUpdate)
	register("show", "show a task or group: at show REF [--full]", runShow)
	register("list", "list the plan: at list [ready|blocked|all|archived]", runList)
	register("status", "project summary: counts, claimable, active, done, stalled", runStatus)
	register("prune", "remove worktrees of complete/archived tasks and quarantined directories of idle tasks", runPrune)
}

// stringList collects a repeatable flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// planFlags are shared by add and update.
type planFlags struct {
	expectRev      uint64
	idempotencyKey string
	planner        bool
	session        string
}

func (c *ctxt) bindPlanFlags(pf *planFlags) {
	c.fs.Uint64Var(&pf.expectRev, "expect-rev", 0, "fail with PLAN_CONFLICT unless the plan revision equals this")
	c.fs.StringVar(&pf.idempotencyKey, "idempotency-key", "", "stable operation id; a replay returns the stored result")
	c.fs.BoolVar(&pf.planner, "planner", false, "assert planning authority for edits to claimed tasks")
	c.fs.StringVar(&pf.session, "session", "", "session token authorising a discovered blocker (default: AT_SESSION, or the task worktree's own token)")
}

// changeSet builds the ChangeSet; session authority comes from --session,
// AT_SESSION, or the worktree the command runs in (same sources as the
// execution verbs, so `add --blocks` works from inside the worktree).
func (c *ctxt) changeSet(ctx context.Context, pid project.ID, pf planFlags, ops ...plan.Change) plan.ChangeSet {
	session := pf.session
	if session == "" {
		session = c.env.Getenv("AT_SESSION")
	}
	if session == "" {
		session = workspace.LoadToken(ctx, c.env.Cwd)
	}
	return plan.ChangeSet{ProjectID: pid, ExpectedPlanRev: pf.expectRev, IdempotencyKey: pf.idempotencyKey, Planner: pf.planner, Session: session, Operations: ops}
}

func runInit(ctx context.Context, c *ctxt, args []string) error {
	name := c.fs.String("name", "", "display name (default: directory name)")
	path := c.fs.String("path", "", "directory to register (default: current directory)")
	noInstructions := c.fs.Bool("no-instructions", false, "do not write AGENTS.md / CLAUDE.md")
	claude := c.fs.Bool("claude", false, "also install the Claude Code hooks (.claude/settings.json: context at session start, edits kept in task worktrees, no stopping with an unfinished claim)")
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
		ClaudeHooks  string             `json:"claude_hooks,omitempty"`
	}
	o := out{InitResult: res}
	if !*noInstructions {
		ir, err := app.InstallInstructions(p.RootPath)
		if err != nil {
			return err
		}
		o.Instructions = &ir
	}
	hooksChanged := false
	if *claude {
		path, changed, err := app.InstallClaudeHooks(p.RootPath)
		if err != nil {
			return err
		}
		o.ClaudeHooks, hooksChanged = path, changed
	}
	return c.emit(o, func(w io.Writer) {
		if res.Existing {
			fmt.Fprintf(w, "✓ Project %s · %s (already registered)\n", p.ID, p.Name)
		} else {
			fmt.Fprintf(w, "✓ Registered project %s · %s\n", p.ID, p.Name)
		}
		fmt.Fprintf(w, "Root:        %s\n", p.RootPath)
		if res.CreatedRepo {
			fmt.Fprintln(w, "Repository:  created (git init + initial commit)")
		}
		fmt.Fprintf(w, "Target:      %s (integration: %s; task branches at/<id>)\n", p.TargetBranch, p.Integration)
		fmt.Fprintf(w, "State:       %s\n", e.Path())
		if ir := o.Instructions; ir != nil {
			switch {
			case ir.Created:
				fmt.Fprintf(w, "Agents:      wrote %s (edit the guidelines below the at block) and CLAUDE.md (@AGENTS.md)\n", ir.AgentsMD)
			case ir.Updated:
				fmt.Fprintf(w, "Agents:      refreshed the at block in %s\n", ir.AgentsMD)
			default:
				fmt.Fprintf(w, "Agents:      %s is current\n", ir.AgentsMD)
			}
		}
		if o.ClaudeHooks != "" {
			if hooksChanged {
				fmt.Fprintf(w, "Claude Code: hooks installed in %s\n", o.ClaudeHooks)
			} else {
				fmt.Fprintf(w, "Claude Code: hooks already in %s\n", o.ClaudeHooks)
			}
		}
		if len(p.Regression) == 0 {
			fmt.Fprintln(w, "Next:        define regression checks with `at project --regression-check \"id: cmd\"` (required before any claim), then `at doctor`")
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
	return c.emit(ps, func(w io.Writer) {
		for _, p := range ps {
			fmt.Fprintf(w, "%s  %-20s  %s\n", p.ID, p.Name, p.RootPath)
		}
	})
}

func runProject(ctx context.Context, c *ctxt, args []string) error {
	name := c.fs.String("name", "", "rename the project")
	integration := c.fs.String("integration", "", "integration policy: none|promote")
	target := c.fs.String("target-branch", "", "branch verified work is promoted to")
	regression := c.fs.String("regression-json", "", "project regression checks as a JSON array of checks")
	regressionFile := c.fs.String("regression-file", "", "read --regression-json from a file")
	var regChecks stringList
	c.fs.Var(&regChecks, "regression-check", "regression check \"[id:] command args...\" (repeatable; replaces the list)")
	maxAttempts := c.fs.Int("max-attempts", -1, "failed attempts before a task needs attention (0 = unlimited)")
	budgetSmall := c.fs.String("budget-small", "", "task-check budget for small tasks (default 30s; 'none' = unlimited)")
	budgetMedium := c.fs.String("budget-medium", "", "task-check budget for medium tasks (default 5m; 'none' = unlimited)")
	budgetLarge := c.fs.String("budget-large", "", "task-check budget for large tasks (default 15m; 'none' = unlimited)")
	regressionBudget := c.fs.String("regression-budget", "", "budget for the regression suite (default 2m; 'none' = unlimited)")
	cooldown := c.fs.Duration("retry-cooldown", -1, "delay before re-claiming a failed task")
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
	if *name != "" {
		p.Name, changed = *name, true
	}
	if *integration != "" {
		p.Integration, changed = project.IntegrationPolicy(*integration), true
	}
	if *target != "" {
		p.TargetBranch, changed = *target, true
	}
	if *regressionFile != "" {
		b, err := os.ReadFile(*regressionFile)
		if err != nil {
			return usage("read %s: %v", *regressionFile, err)
		}
		*regression = string(b)
	}
	if *regression != "" {
		var checks []verification.CheckSpec
		if err := json.Unmarshal([]byte(*regression), &checks); err != nil {
			return usage("--regression-json: %v", err)
		}
		p.Regression, changed = checks, true
	}
	if len(regChecks) > 0 {
		p.Regression = nil
		for i, raw := range regChecks {
			cs, err := parseCheck(raw, i+1, true)
			if err != nil {
				return err
			}
			p.Regression = append(p.Regression, cs)
		}
		changed = true
	}
	if *maxAttempts >= 0 {
		p.MaxAttempts, changed = *maxAttempts, true
	}
	for _, b := range []struct {
		flag string
		dst  *time.Duration
	}{{*budgetSmall, &p.Budgets.Small}, {*budgetMedium, &p.Budgets.Medium}, {*budgetLarge, &p.Budgets.Large}, {*regressionBudget, &p.Budgets.Regression}} {
		if b.flag == "" {
			continue
		}
		d, err := parseBudget(b.flag)
		if err != nil {
			return err
		}
		*b.dst, changed = d, true
	}
	if *cooldown >= 0 {
		p.RetryCooldown, changed = *cooldown, true
	}
	if changed {
		if err := e.UpdateProject(ctx, p); err != nil {
			return err
		}
		if p, err = e.Project(ctx, pid); err != nil {
			return err
		}
	}
	return c.emit(p, func(w io.Writer) {
		fmt.Fprintf(w, "Project:      %s · %s\nRoot:         %s\nIntegration:  %s\nTarget:       %s\nPlan rev:     %d\nMax attempts: %d\nCooldown:     %s\nBudgets:      small %s · medium %s · large %s · regression %s\nRegression:   %d check(s)\n", p.ID, p.Name, p.RootPath, p.Integration, p.TargetBranch, p.PlanRev, p.MaxAttempts, p.RetryCooldown, budgetText(p.Budgets.ForSize("small")), budgetText(p.Budgets.ForSize("medium")), budgetText(p.Budgets.ForSize("large")), budgetText(p.Budgets.RegressionBudget()), len(p.Regression))
		for _, ch := range p.Regression {
			fmt.Fprintf(w, "  - %s: %s\n", ch.ID, strings.Join(ch.Command, " "))
		}
	})
}

var checkIDPrefix = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// parseCheck reads "[id:] command args..." into a CheckSpec. A label is
// recognised only as "<id>: " (an id followed by a colon and a space), so
// a command such as "https://host/x" is never split at its colon.
func parseCheck(raw string, n int, required bool) (verification.CheckSpec, error) {
	id := fmt.Sprintf("check%d", n)
	raw = strings.TrimSpace(raw)
	cmd := raw
	if i := strings.Index(raw, ":"); i > 0 && i+1 < len(raw) && (raw[i+1] == ' ' || raw[i+1] == '\t') && checkIDPrefix.MatchString(raw[:i]) {
		id, cmd = raw[:i], raw[i+1:]
	}
	argv, err := splitWords(cmd)
	if err != nil || len(argv) == 0 {
		return verification.CheckSpec{}, usage("--check %q: expected \"[id:] command args...\"", raw)
	}
	return verification.CheckSpec{ID: id, Command: argv, Required: required}, nil
}

func refs(raw stringList) []plan.Ref {
	var out []plan.Ref
	for _, r := range raw {
		out = append(out, plan.Ref(strings.TrimSpace(r)))
	}
	return out
}

func runAdd(ctx context.Context, c *ctxt, args []string) error {
	var pf planFlags
	c.bindPlanFlags(&pf)
	key := c.fs.String("key", "", "stable project-local key")
	group := c.fs.Bool("group", false, "create an organizational group instead of a task")
	parent := c.fs.String("parent", "", "enclosing group (id or key)")
	outcome := c.fs.String("outcome", "", "the single intended outcome (default: the title)")
	cohort := c.fs.String("cohort", "", "coupled-verification cohort name")
	var constraints, accept, checks, optionalChecks, requires, blocks, pins stringList
	c.fs.Var(&constraints, "constraint", "constraint (repeatable)")
	c.fs.Var(&pins, "pin", "path (file, or directory prefix) this task may not change; verify complete refuses if it did (repeatable)")
	size := c.fs.String("size", "", "how long the task's checks may take: small (default, 30s), medium (5m) or large (15m); the project's budgets set the seconds")
	c.fs.Var(&accept, "accept", "acceptance criterion (repeatable)")
	c.fs.Var(&checks, "check", "required task check: \"[id:] command args...\" (repeatable)")
	c.fs.Var(&optionalChecks, "optional-check", "optional task check (repeatable)")
	c.fs.Var(&requires, "requires", "hard prerequisite (id or key, repeatable)")
	c.fs.Var(&blocks, "blocks", "existing task that will require the new one (repeatable)")
	policyJSON := c.fs.String("policy-json", "", "task checks as JSON ({\"task_checks\": [...]})")
	file := c.fs.String("f", "", "plan file (JSON or YAML; '-' for stdin): one item, a list, or {tasks: [...]}; applied atomically")
	inline := c.fs.String("plan", "", "inline JSON plan (same shape as -f)")
	claim := c.fs.Bool("claim", false, "claim the task just added and print its session (single task only)")
	if err := c.parse(args); err != nil {
		return err
	}
	if *file != "" || *inline != "" {
		if len(c.args) != 0 || *key != "" || *group || *outcome != "" || len(checks)+len(optionalChecks)+len(requires)+len(blocks) > 0 {
			return usage("-f/--plan take the whole plan; no title or task flags alongside")
		}
		return c.addFromPlan(ctx, pf, *file, *inline, *claim)
	}
	if len(c.args) != 1 {
		return usage("add needs exactly one title argument (or -f FILE / --json)")
	}
	pid, err := c.resolveProject(ctx)
	if err != nil {
		return err
	}
	var op plan.Change
	if *group {
		if len(checks)+len(optionalChecks)+len(requires)+len(blocks)+len(accept)+len(constraints)+len(pins) > 0 || *cohort != "" || *outcome != "" || *policyJSON != "" || *size != "" {
			return usage("--group takes only --key and --parent; checks, prerequisites, blockers, cohort, outcome, acceptance, constraints and pins belong to tasks")
		}
		op = plan.AddGroup{Key: *key, Title: c.args[0], Parent: plan.Ref(*parent)}
	} else {
		t := plan.AddTask{Key: *key, Title: c.args[0], Outcome: *outcome, Parent: plan.Ref(*parent), Constraints: constraints, Acceptance: accept, Requires: refs(requires), Blocks: refs(blocks), Cohort: *cohort, Pins: pins, Size: *size}
		if *policyJSON != "" {
			var pol verification.Policy
			if err := json.Unmarshal([]byte(*policyJSON), &pol); err != nil {
				return usage("--policy-json: %v", err)
			}
			t.TaskChecks = pol.TaskChecks
		} else {
			for i, raw := range checks {
				cs, err := parseCheck(raw, i+1, true)
				if err != nil {
					return err
				}
				t.TaskChecks = append(t.TaskChecks, cs)
			}
			for i, raw := range optionalChecks {
				cs, err := parseCheck(raw, len(checks)+i+1, false)
				if err != nil {
					return err
				}
				t.TaskChecks = append(t.TaskChecks, cs)
			}
		}
		op = t
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	res, err := e.Apply(ctx, c.changeSet(ctx, pid, pf, op))
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
	return c.emitAdd(ctx, pid, res, id, c.args[0], *claim && !*group)
}

// addOut is the acknowledgement of `at add`: the plan result, the task
// (single-item adds), and the session when --claim was given.
type addOut struct {
	plan.Result
	Task    *app.TaskView `json:"task,omitempty"`
	Session *app.Session  `json:"session,omitempty"`
}

func (c *ctxt) emitAdd(ctx context.Context, pid project.ID, res plan.Result, id task.ID, title string, claim bool) error {
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
	if claim {
		if id == "" {
			return fault.New(fault.CodeInvalidInput, "--claim needs exactly one task to claim")
		}
		s, err := e.Claim(ctx, app.ClaimRequest{ProjectID: pid, TaskID: &id})
		if err != nil {
			return err
		}
		o.Session = &s
	}
	return c.emit(o, func(w io.Writer) {
		verb := "Created"
		if res.Replayed || res.Changed == 0 {
			verb = "Unchanged"
		}
		if id != "" {
			fmt.Fprintf(w, "✓ %s %s · %s\n", verb, id, title)
		} else {
			fmt.Fprintf(w, "✓ %s %d item(s)\n", verb, len(res.Created))
			for name, cid := range res.Created {
				fmt.Fprintf(w, "  %s  %s\n", cid, name)
			}
		}
		if o.Task != nil && o.Session == nil {
			renderAck(w, *o.Task)
		}
		renderWarnings(w, res.Warnings)
		fmt.Fprintf(w, "Plan rev: %d\n", res.PlanRev)
		if o.Session != nil {
			fmt.Fprintln(w)
			renderSession(w, *o.Session)
		}
	})
}

// addFromPlan applies a plan file or inline JSON as one atomic revision.
func (c *ctxt) addFromPlan(ctx context.Context, pf planFlags, file, inline string, claim bool) error {
	var items []planItem
	var err error
	if inline != "" {
		items, err = readPlanItems("--plan", strings.NewReader(inline))
	} else {
		name, r, oerr := openPlanFile(file, c.env.Stdin)
		if oerr != nil {
			return oerr
		}
		defer r.Close()
		items, err = readPlanItems(name, r)
	}
	if err != nil {
		return err
	}
	pid, err := c.resolveProject(ctx)
	if err != nil {
		return err
	}
	ops := make([]plan.Change, 0, len(items))
	for i, it := range items {
		op, err := it.change(i)
		if err != nil {
			return err
		}
		ops = append(ops, op)
	}
	if claim && len(items) != 1 {
		return usage("--claim takes a plan with exactly one task")
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	res, err := e.Apply(ctx, c.changeSet(ctx, pid, pf, ops...))
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
	return c.emitAdd(ctx, pid, res, id, title, claim)
}

func runUpdate(ctx context.Context, c *ctxt, args []string) error {
	var pf planFlags
	c.bindPlanFlags(&pf)
	title := c.fs.String("title", "", "new title")
	outcome := c.fs.String("outcome", "", "new outcome")
	parent := c.fs.String("parent", "\x00", "move into a group (id or key); empty string clears")
	cohort := c.fs.String("cohort", "\x00", "set cohort; empty string clears")
	var constraints, accept, checks, optionalChecks, requires, remove, setRequires, pins stringList
	c.fs.Var(&constraints, "constraint", "replace constraints (repeatable)")
	c.fs.Var(&pins, "pin", "replace pinned paths (repeatable; use once with empty value to clear)")
	size := c.fs.String("size", "", "set the size: small, medium or large")
	c.fs.Var(&accept, "accept", "replace acceptance criteria (repeatable)")
	c.fs.Var(&checks, "check", "replace task checks (repeatable)")
	c.fs.Var(&optionalChecks, "optional-check", "optional task check (repeatable, with --check)")
	c.fs.Var(&requires, "requires", "add a hard prerequisite (repeatable)")
	c.fs.Var(&remove, "remove-requires", "remove a hard prerequisite (repeatable)")
	c.fs.Var(&setRequires, "set-requires", "replace the prerequisite set (repeatable; use once with empty value to clear)")
	archive := c.fs.Bool("archive", false, "archive (soft-delete) the task or group; needs --reason")
	reason := c.fs.String("reason", "", "why the item is archived (recorded; required with --archive)")
	reset := c.fs.Bool("reset-attempts", false, "clear failure bookkeeping so the task can be claimed again")
	withdraw := c.fs.Bool("withdraw-submission", false, "withdraw a cohort member's submission that awaits its peers so its contract can change (with --planner)")
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
		if *title != "" || *outcome != "" || *parent != "\x00" || *cohort != "\x00" || len(constraints)+len(accept)+len(checks)+len(optionalChecks)+len(requires)+len(remove)+len(setRequires)+len(pins) > 0 || *size != "" || *reset || *withdraw {
			return usage("--archive cannot be combined with other edits; archive in its own update")
		}
		ops = append(ops, plan.ArchiveTask{Target: target, Reason: *reason})
	} else {
		u := plan.UpdateTask{Target: target, ResetAttempts: *reset, WithdrawSubmission: *withdraw}
		if *title != "" {
			u.Title = title
		}
		if *outcome != "" {
			u.Outcome = outcome
		}
		if *parent != "\x00" {
			p := plan.Ref(*parent)
			u.Parent = &p
		}
		if *cohort != "\x00" {
			u.Cohort = cohort
		}
		if len(constraints) > 0 {
			u.Constraints = plan.Replace([]string(constraints))
		}
		if len(pins) > 0 {
			var set []string
			for _, p := range pins {
				if strings.TrimSpace(p) != "" {
					set = append(set, p)
				}
			}
			u.Pins = plan.Replace(set)
		}
		if *size != "" {
			u.Size = size
		}
		if len(accept) > 0 {
			u.Acceptance = plan.Replace([]string(accept))
		}
		if len(checks)+len(optionalChecks) > 0 {
			var list []verification.CheckSpec
			for i, raw := range checks {
				cs, err := parseCheck(raw, i+1, true)
				if err != nil {
					return err
				}
				list = append(list, cs)
			}
			for i, raw := range optionalChecks {
				cs, err := parseCheck(raw, len(checks)+i+1, false)
				if err != nil {
					return err
				}
				list = append(list, cs)
			}
			u.TaskChecks = plan.Replace(list)
		}
		u.AddRequires = refs(requires)
		u.RemoveRequires = refs(remove)
		if len(setRequires) > 0 {
			var set []plan.Ref
			for _, r := range setRequires {
				if strings.TrimSpace(r) != "" {
					set = append(set, plan.Ref(r))
				}
			}
			u.Requires = plan.Replace(set)
		}
		ops = append(ops, u)
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	res, err := e.Apply(ctx, c.changeSet(ctx, pid, pf, ops...))
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
			fmt.Fprintf(w, "✓ %s %s · %s\n", verb, view.ID, view.Title)
			renderAck(w, view)
		} else {
			fmt.Fprintf(w, "✓ %s %s\n", verb, target)
		}
		renderWarnings(w, res.Warnings)
		fmt.Fprintf(w, "Plan rev: %d\n", res.PlanRev)
	})
}

func runShow(ctx context.Context, c *ctxt, args []string) error {
	full := c.fs.Bool("full", false, "include full history, runs and evidence")
	if err := c.parse(args); err != nil {
		return err
	}
	if len(c.args) != 1 {
		return usage("show needs exactly one task id or key")
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
	return c.emit(v, func(w io.Writer) { renderShow(w, v, *full) })
}

func runList(ctx context.Context, c *ctxt, args []string) error {
	if err := c.parse(args); err != nil {
		return err
	}
	filter := app.FilterOpen
	if len(c.args) > 1 {
		return usage("list takes at most one filter: ready, blocked, all, archived")
	}
	if len(c.args) == 1 {
		switch c.args[0] {
		case "ready", "blocked", "all", "archived", "open":
			filter = app.Filter(c.args[0])
		default:
			return usage("unknown list filter %q (ready, blocked, all, archived)", c.args[0])
		}
	}
	pid, err := c.resolveProject(ctx)
	if err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	snap, err := e.List(ctx, app.ListQuery{ProjectID: pid, Filter: filter})
	if err != nil {
		return err
	}
	return c.emit(snap, func(w io.Writer) { renderList(w, snap, filter) })
}

func runStatus(ctx context.Context, c *ctxt, args []string) error {
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
	s, err := e.Summary(ctx, pid)
	if err != nil {
		return err
	}
	return c.emit(s, func(w io.Writer) { renderSummary(w, s) })
}

var _ = time.Second

// parseBudget reads a budget flag: a duration, or "none" for no cap.
func parseBudget(s string) (time.Duration, error) {
	if strings.EqualFold(strings.TrimSpace(s), "none") {
		return project.Unlimited, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, usage("budget %q: expected a duration such as 30s or 2m, or 'none'", s)
	}
	return d, nil
}

func budgetText(d time.Duration) string {
	if d <= 0 {
		return "unlimited"
	}
	return d.String()
}

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
		fmt.Fprintf(w, "✓ Pruned %d director%s\n", len(res.Removed), map[bool]string{true: "y", false: "ies"}[len(res.Removed) == 1])
		for _, p := range res.Removed {
			fmt.Fprintf(w, "  - %s\n", p)
		}
	})
}
