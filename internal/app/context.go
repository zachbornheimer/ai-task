package app

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/proof"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

// Context is one task's working context for a coding agent: the prompt a
// host hands the agent, plus the facts it was built from. The text is the
// same for every harness; the agent only needs a shell with `atask` on PATH.
type Context struct {
	Prompt       string                   `json:"prompt"`
	Project      project.ID               `json:"project"`
	Task         *TaskView                `json:"task,omitempty"`
	Workspace    string                   `json:"workspace,omitempty"`
	Branch       string                   `json:"branch,omitempty"`
	LeaseUntil   *time.Time               `json:"lease_until,omitempty"`
	TargetBranch string                   `json:"target_branch"`
	Regression   []verification.CheckSpec `json:"regression_checks"`
	// ChangedFiles are the files the task branch has changed against the
	// target so far (earlier attempts' work); DirtyFiles are uncommitted
	// changes left in the workspace.
	ChangedFiles []string `json:"changed_files,omitempty"`
	DirtyFiles   []string `json:"dirty_files,omitempty"`
	// Siblings are the project's other open tasks: what not to implement.
	Siblings []Rel `json:"siblings,omitempty"`
	// Worktrunk reports that `wt` is on PATH, so the prompt names its
	// commit and rebase steps instead of raw git.
	Worktrunk bool `json:"worktrunk"`
	// Summary and Claimable describe the plan when no task is in hand.
	Summary   *Summary `json:"summary,omitempty"`
	Claimable []Rel    `json:"claimable,omitempty"`
}

// SiblingLimit bounds the open tasks listed in a context.
const SiblingLimit = 15

// Context builds the working context for a task, given either a
// reference (any task, read-only) or a session token (that session's
// task). With withRules the standing rules (AGENTS.md) are appended, for
// a headless agent that never reads that file.
func (e *Engine) Context(ctx context.Context, pid project.ID, ref string, token execution.Token, withRules bool) (Context, error) {
	if ref == "" && token == "" {
		return e.projectContext(ctx, pid, withRules)
	}
	if err := e.reconcile(ctx); err != nil {
		return Context{}, err
	}
	now := e.now()
	var (
		v          TaskView
		proj       project.Project
		sibs       []Rel
		regression []verification.CheckSpec
	)
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		var r sqlite.Record
		var err error
		if ref != "" {
			r, err = e.lookup(tx, pid, ref)
		} else {
			var auth sqlite.AttemptAuth
			auth, err = tx.AttemptByDigest(token.Digest())
			if err == nil {
				r, err = tx.GetRecord(auth.Attempt.TaskID)
			}
		}
		if err != nil {
			return err
		}
		if r.Task.Kind == task.KindGroup {
			return fault.New(fault.CodeInvalidInput, "%s is a group; context describes an executable task", r.Task.ID)
		}
		if proj, err = tx.GetProject(r.Task.ProjectID); err != nil {
			return err
		}
		if v, err = e.buildView(tx, r, proj, now, true, targetHead{}); err != nil {
			return err
		}
		regression = append([]verification.CheckSpec{}, r.Task.Verification.Regression...)
		// The same enrichment the claim prints: prerequisite learnings
		// and the last failed verification.
		if v.Handoff == nil {
			v.Handoff = &execution.Handoff{}
		}
		if err := e.inheritContext(tx, r, v.Handoff); err != nil {
			return err
		}
		recs, err := tx.ListRecords(proj.ID, sqlite.ScopeOpen, now, proj.MaxAttempts)
		if err != nil {
			return err
		}
		for _, o := range recs {
			if o.Task.ID == r.Task.ID || o.Task.Kind == task.KindGroup {
				continue
			}
			sibs = append(sibs, rel(o, o.Status(now, proj.MaxAttempts)))
		}
		return nil
	})
	if err != nil {
		return Context{}, err
	}
	sort.Slice(sibs, func(i, j int) bool { return sibs[i].ID < sibs[j].ID })
	if len(sibs) > SiblingLimit {
		sibs = sibs[:SiblingLimit]
	}
	c := Context{Project: proj.ID, Task: &v, TargetBranch: proj.TargetBranch, Regression: regression, Siblings: sibs}
	if a := v.Attempt; a != nil && a.EndedAt == nil {
		c.Workspace, c.Branch = a.Workspace, a.Branch
		lease := a.LeaseExpiresAt
		c.LeaseUntil = &lease
	}
	_, wtErr := exec.LookPath("wt")
	c.Worktrunk = wtErr == nil
	if c.Workspace != "" {
		if st, err := workspace.Inspect(ctx, c.Workspace); err == nil {
			for _, line := range st.Dirty {
				// Porcelain lines are "XY path"; the path is what matters.
				if len(line) > 3 {
					c.DirtyFiles = append(c.DirtyFiles, line[3:])
				}
			}
		}
		c.ChangedFiles, _ = workspace.ChangedFiles(ctx, c.Workspace, "refs/heads/"+proj.TargetBranch)
	}
	c.Prompt = renderContext(c)
	if withRules {
		c.Prompt += "\n" + Rules
	}
	return c, nil
}

// projectContext is what a session with no task in hand gets: the state
// of the plan and what to do next (claim, or plan).
func (e *Engine) projectContext(ctx context.Context, pid project.ID, withRules bool) (Context, error) {
	var proj project.Project
	if pid != "" {
		p, err := e.Project(ctx, pid)
		if err != nil {
			return Context{}, err
		}
		proj = p
	} else {
		proj = project.Project{Name: project.GeneralName, MaxAttempts: project.DefaultMaxAttempts}
	}
	sum, err := e.Summary(ctx, pid)
	if err != nil {
		return Context{}, err
	}
	snap, err := e.List(ctx, ListQuery{ProjectID: pid, Filter: FilterReady})
	if err != nil {
		return Context{}, err
	}
	c := Context{Project: proj.ID, TargetBranch: proj.TargetBranch, Regression: []verification.CheckSpec{}, Summary: &sum}
	for _, t := range snap.Tasks {
		c.Claimable = append(c.Claimable, Rel{ID: t.ID, Key: t.Key, Title: t.Title, Status: t.Status})
	}
	_, wtErr := exec.LookPath("wt")
	c.Worktrunk = wtErr == nil
	var w strings.Builder
	if proj.IsGeneral() {
		w.WriteString("# Board: no task in hand\n\n")
	} else {
		fmt.Fprintf(&w, "# Project %s: no task in hand\n\n", proj.Name)
	}
	fmt.Fprintf(&w, "Plan: %d task(s), %d open, %d claimable, %d active", sum.Total, sum.Open, sum.Claimable, sum.Active)
	if sum.Cooling > 0 {
		fmt.Fprintf(&w, ", %d cooling down", sum.Cooling)
	}
	w.WriteString(".\n")
	switch {
	case sum.Total == 0:
		w.WriteString("Nothing is planned yet. Turn the request into tasks, each with a proof and a regression (`atask add \"title\" --proof \"run: cmd\" --regression \"run: cmd\"`, several at once with `atask add -f plan.yaml` or `--plan`), then claim one. `atask add ... --claim` adds and claims a single task in one step.")
		if !proj.IsGeneral() {
			w.WriteString(" Run `atask doctor` first in a repository you have not worked in.")
		}
		w.WriteByte('\n')
	case sum.Done:
		w.WriteString("Every task is complete. New work needs new tasks (`atask add`).\n")
	case len(c.Claimable) > 0:
		w.WriteString("Claimable now (`atask claim <id>`, or `atask claim --wait` for the oldest):\n")
		for _, r := range c.Claimable {
			fmt.Fprintf(&w, "- %s %s\n", r.ID, r.Title)
		}
	case sum.Stalled:
		w.WriteString("The plan is stalled; a planner must act:\n")
		for _, r := range sum.Reasons {
			fmt.Fprintf(&w, "- %s\n", r)
		}
	default:
		w.WriteString("Nothing is claimable right now (work is in flight or cooling down); `atask claim --wait` blocks until something is.\n")
	}
	if proj.TargetBranch != "" {
		fmt.Fprintf(&w, "\nAfter claiming, `cd` into the workspace the claim prints and run `atask show` there. Never edit %s directly.\n", proj.TargetBranch)
	} else {
		w.WriteString("\nClaim with `atask claim <id>`. Work in general completes without git.\n")
	}
	c.Prompt = w.String()
	if withRules {
		c.Prompt += "\n" + Rules
	}
	return c, nil
}

func renderContext(c Context) string {
	var w strings.Builder
	v := *c.Task
	fmt.Fprintf(&w, "# Task %s: %s\n\n", v.ID, v.Title)
	if v.Status == task.StatusComplete {
		fmt.Fprintf(&w, "This task is complete")
		if v.CompletedRevision != "" {
			fmt.Fprintf(&w, " (revision %s)", short(v.CompletedRevision))
		}
		w.WriteString(". Nothing is left to do; do not implement or commit further work.\n")
		if v.Outcome != "" && v.Outcome != v.Title {
			fmt.Fprintf(&w, "\nOutcome: %s\n", v.Outcome)
		}
		return w.String()
	}
	if v.Outcome != "" && v.Outcome != v.Title {
		fmt.Fprintf(&w, "Outcome: %s\n\n", v.Outcome)
	}
	// A retry reads its state before the contract: what exists, what the
	// last attempt learned, and why it did not complete.
	retry := v.Attempt != nil && v.Attempt.Seq > 1
	if retry {
		fmt.Fprintf(&w, "## Current state (attempt %d)\n\nResume from here; do not redo finished work.\n", v.Attempt.Seq)
		renderState(&w, c)
		w.WriteString("\n")
	}
	if len(v.Constraints) > 0 {
		w.WriteString("Constraints:\n")
		for _, c := range v.Constraints {
			fmt.Fprintf(&w, "- %s\n", c)
		}
		w.WriteString("\n")
	}
	if len(v.Acceptance) > 0 {
		w.WriteString("Acceptance criteria:\n")
		for _, a := range v.Acceptance {
			fmt.Fprintf(&w, "- %s\n", a.Description)
		}
		w.WriteString("\n")
	}
	w.WriteString("Done when `atask verify complete` passes on the committed revision: every required task check, then every regression check, run fresh. The checks are the contract.\n")
	size := v.Size
	if size == "" {
		size = task.SizeSmall
	}
	fmt.Fprintf(&w, "Budgets: task checks %s (size %s), regression %s; a check that runs out of time fails the run as a timeout (never a strike) and means the verification needs fixing, not the code. Checks get ATASK_CHANGED_FILES (a file listing the paths this change touched) and ATASK_TARGET_BRANCH.\n\n", budgetLabelFor(proof.Size(size).Budget()), size, budgetLabelFor(regressionBudgetOf(v.Regression, size)))
	if len(v.Checks) > 0 {
		w.WriteString("Task checks (`atask verify proof`):\n")
		for _, ch := range v.Checks {
			req := "required"
			if !ch.Required {
				req = "optional"
			}
			fmt.Fprintf(&w, "- %s (%s): `%s`\n", ch.ID, req, CheckText(ch))
		}
		w.WriteString("\n")
	}
	w.WriteString("Regression checks (`atask verify regression`):\n")
	for _, ch := range c.Regression {
		fmt.Fprintf(&w, "- %s: `%s`\n", ch.ID, CheckText(ch))
	}
	w.WriteString("\n")
	if len(v.Requires) > 0 {
		w.WriteString("Prerequisites (complete; their work is on the target branch):\n")
		for _, r := range v.Requires {
			fmt.Fprintf(&w, "- %s %s\n", r.ID, r.Title)
		}
		w.WriteString("\n")
	}
	if len(v.Blocks) > 0 {
		w.WriteString("Blocks:\n")
		for _, r := range v.Blocks {
			fmt.Fprintf(&w, "- %s %s [%s]\n", r.ID, r.Title, r.Status)
		}
		w.WriteString("\n")
	}
	if c.Workspace != "" {
		fmt.Fprintf(&w, "Workspace: %s (branch %s; it started from %s).", c.Workspace, c.Branch, c.TargetBranch)
		if c.LeaseUntil != nil {
			fmt.Fprintf(&w, " Lease until %s.", c.LeaseUntil.UTC().Format(time.RFC3339))
		}
		w.WriteString("\n")
		if !retry {
			renderState(&w, c)
		}
	} else {
		fmt.Fprintf(&w, "Completion records the task branch revision; it does not move %s. ", c.TargetBranch)
		switch v.Status {
		case task.StatusComplete:
			fmt.Fprintf(&w, "This task is complete (revision %s); nothing is left to do.\n", short(v.CompletedRevision))
		case task.StatusReady, task.StatusInterrupted, task.StatusVerificationFailed:
			fmt.Fprintf(&w, "Nobody holds this task; `atask claim %s` gives you a workspace.\n", v.ID)
		default:
			fmt.Fprintf(&w, "This task is %s and cannot be claimed right now.\n", v.Status)
		}
		if !retry {
			renderState(&w, c)
		}
	}
	if c.Worktrunk {
		fmt.Fprintf(&w, "Commit with `wt step commit` (Worktrunk is installed: `wt step diff` shows all changes since branching, `wt step squash` folds commits; on BASE_CONFLICT use `wt step rebase %s`).\n", c.TargetBranch)
	} else {
		fmt.Fprintf(&w, "Commit with git; on BASE_CONFLICT use `git merge %s`.\n", c.TargetBranch)
	}
	return w.String()
}

// renderState writes what exists already: files the branch changed,
// uncommitted edits, and the handoff from earlier attempts.
func renderState(w *strings.Builder, c Context) {
	if len(c.ChangedFiles) > 0 {
		fmt.Fprintf(w, "Files this branch has changed: %s\n", strings.Join(c.ChangedFiles, ", "))
	}
	if len(c.DirtyFiles) > 0 {
		fmt.Fprintf(w, "Uncommitted changes in the workspace (review before editing): %s\n", strings.Join(c.DirtyFiles, ", "))
	}
	h := c.Task.Handoff
	if h == nil || h.Empty() {
		return
	}
	if h.LatestNext != "" {
		fmt.Fprintf(w, "Next step recorded by the last attempt: %s\n", h.LatestNext)
	}
	for _, l := range h.Learnings {
		fmt.Fprintf(w, "- learned: %s\n", l)
	}
	for _, l := range h.Inherited {
		fmt.Fprintf(w, "- learned on prerequisite %s: %s\n", l.TaskID, l.Learned)
	}
	if f := h.LastFailure; f != nil {
		fmt.Fprintf(w, "Last `atask verify %s`: %s\n", f.Mode, f.Summary)
		for _, ch := range f.Checks {
			fmt.Fprintf(w, "- %s %s", ch.CheckID, ch.Outcome)
			if ch.Message != "" {
				fmt.Fprintf(w, ": %s", ch.Message)
			}
			w.WriteString("\n")
			for _, tail := range []struct{ name, text string }{{"stdout", ch.StdoutTail}, {"stderr", ch.StderrTail}} {
				if t := strings.TrimSpace(tail.text); t != "" {
					fmt.Fprintf(w, "  %s: %s\n", tail.name, strings.ReplaceAll(t, "\n", "\n  "))
				}
			}
			if ch.Log != "" {
				fmt.Fprintf(w, "  full output: %s\n", ch.Log)
			}
		}
	}
}

// ShellJoin renders an argv as a shell would need it typed, quoting words
// that hold spaces or shell syntax, so a check is shown as it really runs.
func ShellJoin(argv []string) string { return shellJoin(argv) }

// shellJoin renders an argv the way a shell would need it typed, so a
// check command in a prompt is copy-pastable.
func shellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && !strings.ContainsAny(a, " \t\n'\"\\$`*?[]{}()<>|&;#~") {
			parts[i] = a
			continue
		}
		parts[i] = "'" + strings.ReplaceAll(a, "'", "'\\''") + "'"
	}
	return strings.Join(parts, " ")
}

// Rules is the long execution contract for hosts that pass withRules to
// Engine.Context. The installed AGENTS.md block is Instructions(), not this.
const Rules = `## Agent execution contract

Complete one independently verifiable task at a time.

Start

- Run ` + "`atask context`" + ` once for the operating contract. ` + "`atask show`" + ` is the current claim; ` + "`atask list`" + ` is the plan. Review the handoff, the existing changes and ` + "`git status`" + ` before editing; preserve useful prior work.
- Work only in the assigned workspace. The session token is stored there, so ` + "`atask`" + ` commands run there need no token.
- Do not implement other tasks.

Execute

- Implement the outcome and satisfy its acceptance criteria.
- The checks are the contract: never weaken, skip or bypass a check or the tests it runs.
- Iterate with ` + "`atask verify proof`" + ` and ` + "`atask verify regression`" + ` (diagnostic; they never complete anything).
- Record progress with ` + "`atask log --done \"...\" --next \"...\" --learned \"...\"`" + `; ` + "`--next`" + ` is what the next attempt reads first.
- The lease lasts 30 minutes and every ` + "`atask`" + ` command renews it; log before it runs out.
- On INVALID_SESSION, stop editing immediately. The next ` + "`atask claim <task>`" + ` returns your handoff.

Complete

- Commit before ` + "`atask verify complete`" + ` (` + "`wt step commit`" + ` where Worktrunk is installed, plain git otherwise). The tree must be clean; nothing is committed for you.
- You never set status. Only a passing ` + "`atask verify complete`" + ` completes the task: it runs every required task check and regression check fresh on the committed revision, and records the revision; any on_complete actions then run.
- On CHECK_FAILED, read ` + "`error.details`" + `, fix the cause, commit, verify again. Your claim stays live.
- On BASE_CONFLICT, the target moved against you: ` + "`wt step rebase <target>`" + ` (or ` + "`git merge <target>`" + `), resolve ` + "`error.details.conflicts`" + `, commit, verify again. It is not counted against you.
- If completion depends on work that is not yours: ` + "`atask add \"prereq\" --blocks <task> --proof \"run: cmd\" --regression \"run: cmd\"`" + `, ` + "`atask log`" + ` the handoff, ` + "`atask claim release`" + `. Do not implement the blocking task.

Exit

- ` + "`atask claim --wait`" + ` ends with DONE (everything is complete) or STALLED (a planner is needed); both are normal exits for a worker loop.
- Never claim success without a passing final verification.

`

func budgetLabelFor(d time.Duration) string {
	if d <= 0 {
		return "unlimited"
	}
	return d.String()
}

// regressionBudgetOf is the allowance of a regression proof (the task's size
// when the proof declares none).
func regressionBudgetOf(rp *proof.ProofSpec, size string) time.Duration {
	if rp != nil && rp.Size != "" {
		return rp.Size.Budget()
	}
	return proof.Size(size).Budget()
}

// CheckText renders a check as a person would type it: its argv, or for an
// in-process proof its "PRESET: SUBJECT [k=v]" spec.
func CheckText(c verification.CheckSpec) string {
	if c.Eval != nil {
		return c.Eval.String()
	}
	return shellJoin(c.Command)
}
