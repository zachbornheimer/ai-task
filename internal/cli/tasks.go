package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func init() {
	register("init", "register the current directory (or --path) as a project", runInit)
	register("projects", "list registered projects", runProjects)
	register("project", "show or configure the current project", runProject)
	register("add", "add a task: tasks add \"description\" [--outcome ..] [--accept ..] [--check ..]", runAdd)
	register("show", "show a task with dependencies, lease, and handoff", runShow)
	register("list", "list tasks: --available --blocked --in-progress --interrupted --complete --all", runList)
	register("history", "full log history of a task: tasks history <task> [--limit N] [--offset N]", runHistory)
}

// stringList collects a repeatable flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func runInit(ctx context.Context, c *ctxt, args []string) error {
	name := c.fs.String("name", "", "display name (default: directory name)")
	path := c.fs.String("path", "", "directory to register (default: current directory)")
	noDir := c.fs.Bool("no-dir", false, "register a project with no directory")
	if err := c.parse(args); err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	dir := *path
	if dir == "" && !*noDir {
		dir = c.env.Cwd
	}
	if *noDir {
		dir = ""
	}
	p, err := e.InitProject(ctx, *name, dir)
	if err != nil {
		return err
	}
	return c.emit(p, func(w io.Writer) {
		fmt.Fprintf(w, "registered project %s (%s)\n", p.ID, p.Name)
		if p.RootPath != "" {
			fmt.Fprintf(w, "root: %s\n", p.RootPath)
		}
		fmt.Fprintf(w, "state: %s\n", e.Path())
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
	regression := c.fs.String("regression-json", "", "project regression checks as a JSON array of checks")
	regressionFile := c.fs.String("regression-file", "", "read --regression-json from a file")
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
	if changed {
		if err := e.UpdateProject(ctx, p); err != nil {
			return err
		}
		if p, err = e.Project(ctx, pid); err != nil {
			return err
		}
	}
	return c.emit(p, func(w io.Writer) {
		fmt.Fprintf(w, "project:     %s\nname:        %s\nroot:        %s\nintegration: %s\nregression:  %d check(s)\n", p.ID, p.Name, p.RootPath, p.Integration, len(p.Regression))
		for _, ch := range p.Regression {
			fmt.Fprintf(w, "  - %s: %s\n", ch.ID, strings.Join(ch.Command, " "))
		}
	})
}

func runAdd(ctx context.Context, c *ctxt, args []string) error {
	outcome := c.fs.String("outcome", "", "the single intended outcome (default: the description)")
	var constraints, accept, checks, optionalChecks stringList
	c.fs.Var(&constraints, "constraint", "constraint (repeatable)")
	c.fs.Var(&accept, "accept", "acceptance criterion (repeatable)")
	c.fs.Var(&checks, "check", "required task check: \"[id:] command args...\" (repeatable)")
	c.fs.Var(&optionalChecks, "optional-check", "optional task check (repeatable)")
	policyJSON := c.fs.String("policy-json", "", "full verification policy as JSON (overrides --check)")
	if err := c.parse(args); err != nil {
		return err
	}
	if len(c.args) != 1 {
		return usage("add needs exactly one description argument")
	}
	pid, err := c.resolveProject(ctx)
	if err != nil {
		return err
	}
	spec := task.Spec{ProjectID: pid, Description: c.args[0], Outcome: *outcome, Constraints: constraints}
	for _, a := range accept {
		spec.Acceptance = append(spec.Acceptance, task.AcceptanceCriterion{Description: a})
	}
	if *policyJSON != "" {
		if err := json.Unmarshal([]byte(*policyJSON), &spec.Verification); err != nil {
			return usage("--policy-json: %v", err)
		}
	} else {
		for i, raw := range checks {
			cs, err := parseCheck(raw, i+1, true)
			if err != nil {
				return err
			}
			spec.Verification.TaskChecks = append(spec.Verification.TaskChecks, cs)
		}
		for i, raw := range optionalChecks {
			cs, err := parseCheck(raw, len(checks)+i+1, false)
			if err != nil {
				return err
			}
			spec.Verification.TaskChecks = append(spec.Verification.TaskChecks, cs)
		}
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	t, warnings, err := e.Add(ctx, spec)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintf(c.env.Stderr, "warning: %s\n", w)
	}
	type added struct {
		task.Task
		Warnings []string `json:"warnings,omitempty"`
	}
	return c.emit(added{t, warnings}, func(w io.Writer) { fmt.Fprintln(w, t.ID) })
}

// parseCheck reads "[id:] command args..." into a CheckSpec.
func parseCheck(raw string, n int, required bool) (verification.CheckSpec, error) {
	id := fmt.Sprintf("check%d", n)
	cmd := raw
	if i := strings.Index(raw, ":"); i > 0 && !strings.ContainsAny(raw[:i], " \t/\\") {
		id, cmd = raw[:i], raw[i+1:]
	}
	argv, err := splitWords(cmd)
	if err != nil || len(argv) == 0 {
		return verification.CheckSpec{}, usage("--check %q: expected \"[id:] command args...\"", raw)
	}
	return verification.CheckSpec{ID: id, Command: argv, Required: required}, nil
}

func runShow(ctx context.Context, c *ctxt, args []string) error {
	if err := c.parse(args); err != nil {
		return err
	}
	if len(c.args) != 1 {
		return usage("show needs exactly one task id")
	}
	id, err := task.ParseID(c.args[0])
	if err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	v, err := e.Show(ctx, id)
	if err != nil {
		return err
	}
	return c.emit(v, func(w io.Writer) { renderTaskView(w, v) })
}

func renderTaskView(w io.Writer, v app.TaskView) {
	t := v.Task
	fmt.Fprintf(w, "%s  [%s]\n", t.ID, v.Status)
	fmt.Fprintf(w, "description: %s\n", t.Description)
	if t.Outcome != t.Description {
		fmt.Fprintf(w, "outcome:     %s\n", t.Outcome)
	}
	for _, cn := range t.Constraints {
		fmt.Fprintf(w, "constraint:  %s\n", cn)
	}
	for _, a := range t.Acceptance {
		fmt.Fprintf(w, "accept:      %s\n", a.Description)
	}
	for _, ch := range t.Verification.TaskChecks {
		kind := "required"
		if !ch.Required {
			kind = "optional"
		}
		fmt.Fprintf(w, "check:       %s (%s): %s\n", ch.ID, kind, strings.Join(ch.Command, " "))
	}
	for _, d := range v.Requires {
		fmt.Fprintf(w, "requires:    %s [%s] %s\n", d.ID, d.Status, d.Description)
	}
	for _, d := range v.RequiredBy {
		fmt.Fprintf(w, "required by: %s [%s] %s\n", d.ID, d.Status, d.Description)
	}
	if a := v.Attempt; a != nil {
		state := "lease expired"
		if a.LeaseActive {
			state = "lease active until " + a.LeaseExpiresAt.Format(time.RFC3339)
		} else if a.EndedAt != nil {
			state = string(a.EndReason)
		}
		fmt.Fprintf(w, "attempt:     #%d (%s)\n", a.Seq, state)
	}
	if s := v.Submission; s != nil {
		fmt.Fprintf(w, "submission:  #%d from attempt %d, verification %s\n", s.ID, s.AttemptSeq, s.Verification)
	}
	if v.Handoff != nil {
		renderHandoff(w, *v.Handoff)
	}
}

func renderHandoff(w io.Writer, h execution.Handoff) {
	if h.TotalLogs == 0 {
		return
	}
	if h.LatestNext != "" {
		fmt.Fprintf(w, "next:        %s\n", h.LatestNext)
	}
	for _, l := range h.Learnings {
		fmt.Fprintf(w, "learned:     %s\n", l)
	}
	fmt.Fprintf(w, "recent log (%d of %d, newest first):\n", len(h.RecentLogs), h.TotalLogs)
	for _, l := range h.RecentLogs {
		renderLog(w, l)
	}
	for _, wn := range h.Warnings {
		fmt.Fprintf(w, "warning:     %s\n", wn)
	}
}

func renderLog(w io.Writer, l execution.RecordedLog) {
	fmt.Fprintf(w, "  #%d attempt %d %s\n", l.Seq, l.AttemptSeq, l.At.Format(time.RFC3339))
	for _, kv := range []struct{ k, v string }{{"done", l.Done}, {"next", l.Next}, {"learned", l.Learned}, {"note", l.Note}} {
		if kv.v != "" {
			fmt.Fprintf(w, "    %-8s %s\n", kv.k+":", kv.v)
		}
	}
}

func runList(ctx context.Context, c *ctxt, args []string) error {
	flags := map[task.Status]*bool{
		task.StatusAvailable:            c.fs.Bool("available", false, "fresh work whose prerequisites are complete"),
		task.StatusBlocked:              c.fs.Bool("blocked", false, "waiting on prerequisites"),
		task.StatusInProgress:           c.fs.Bool("in-progress", false, "held by an active lease"),
		task.StatusInterrupted:          c.fs.Bool("interrupted", false, "lease expired before finish"),
		task.StatusAwaitingVerification: c.fs.Bool("awaiting-verification", false, "submitted, checks pending"),
		task.StatusVerificationFailed:   c.fs.Bool("verification-failed", false, "submitted, checks failed"),
		task.StatusAwaitingIntegration:  c.fs.Bool("awaiting-integration", false, "verified, not integrated"),
		task.StatusComplete:             c.fs.Bool("complete", false, "complete"),
	}
	takeable := c.fs.Bool("takeable", false, "everything a take could claim: available, interrupted, verification-failed")
	all := c.fs.Bool("all", false, "include complete tasks (default: open tasks only)")
	status := c.fs.String("status", "", "comma-separated statuses")
	if err := c.parse(args); err != nil {
		return err
	}
	var f app.ListFilter
	for st, on := range flags {
		if *on {
			f.Statuses = append(f.Statuses, st)
		}
	}
	if *takeable {
		f.Statuses = append(f.Statuses, task.StatusAvailable, task.StatusInterrupted, task.StatusVerificationFailed)
	}
	for _, s := range strings.Split(*status, ",") {
		if s = strings.TrimSpace(s); s != "" {
			st, ok := task.ParseStatus(s)
			if !ok {
				return usage("unknown status %q", s)
			}
			f.Statuses = append(f.Statuses, st)
		}
	}
	if len(f.Statuses) == 0 && !*all {
		f.Statuses = []task.Status{task.StatusBlocked, task.StatusAvailable, task.StatusInProgress, task.StatusInterrupted, task.StatusAwaitingVerification, task.StatusVerificationFailed, task.StatusAwaitingIntegration}
	}
	pid, err := c.resolveProject(ctx)
	if err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	list, err := e.List(ctx, pid, f)
	if err != nil {
		return err
	}
	if list == nil {
		list = []app.TaskSummary{}
	}
	return c.emit(list, func(w io.Writer) {
		for _, t := range list {
			fmt.Fprintf(w, "%s  %-22s %s\n", t.ID, t.Status, t.Description)
		}
	})
}

func runHistory(ctx context.Context, c *ctxt, args []string) error {
	limit := c.fs.Int("limit", 100, "entries per page")
	offset := c.fs.Int("offset", 0, "entries to skip")
	if err := c.parse(args); err != nil {
		return err
	}
	if len(c.args) != 1 {
		return usage("history needs exactly one task id")
	}
	id, err := task.ParseID(c.args[0])
	if err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	h, err := e.History(ctx, id, *limit, *offset)
	if err != nil {
		return err
	}
	return c.emit(h, func(w io.Writer) {
		fmt.Fprintf(w, "%s: %d log entries, %d attempt(s), %d submission(s)\n", h.TaskID, h.Total, len(h.Attempts), len(h.Submissions))
		for _, a := range h.Attempts {
			end := "open"
			if a.EndedAt != nil {
				end = string(a.EndReason)
			}
			fmt.Fprintf(w, "attempt #%d started %s lease until %s (%s)\n", a.Seq, a.StartedAt.Format(time.RFC3339), a.LeaseExpiresAt.Format(time.RFC3339), end)
		}
		for _, s := range h.Submissions {
			fmt.Fprintf(w, "submission #%d attempt %d at %s verification=%s %s\n", s.ID, s.AttemptSeq, s.SubmittedAt.Format(time.RFC3339), s.Verification, s.Summary)
		}
		for _, l := range h.Entries {
			renderLog(w, l)
		}
	})
}
