package app

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

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
// same for every harness; the agent only needs a shell with `at` on PATH.
type Context struct {
	Prompt       string                   `json:"prompt"`
	Project      project.ID               `json:"project"`
	Task         TaskView                 `json:"task"`
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
}

// SiblingLimit bounds the open tasks listed in a context.
const SiblingLimit = 15

// Context builds the working context for a task, given either a
// reference (any task, read-only) or a session token (that session's
// task). With withRules the standing rules (AGENTS.md) are appended, for
// a headless agent that never reads that file.
func (e *Engine) Context(ctx context.Context, pid project.ID, ref string, token execution.Token, withRules bool) (Context, error) {
	if ref == "" && token == "" {
		return Context{}, fault.New(fault.CodeInvalidInput, "no task here: run `at claim` and `cd` into its workspace, or give a task id")
	}
	if err := e.reconcile(ctx); err != nil {
		return Context{}, err
	}
	now := e.now()
	var (
		v    TaskView
		proj project.Project
		sibs []Rel
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
		if v, err = e.buildView(tx, r, proj, now, true); err != nil {
			return err
		}
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
	c := Context{Project: proj.ID, Task: v, TargetBranch: proj.TargetBranch, Regression: proj.Regression, Siblings: sibs}
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

func renderContext(c Context) string {
	var w strings.Builder
	v := c.Task
	fmt.Fprintf(&w, "# Task %s: %s\n\n", v.ID, v.Title)
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
	w.WriteString("Done when `at verify complete` passes on the committed revision: every required task check, then every regression check, run fresh. The checks are the contract.\n\n")
	if len(v.Checks) > 0 {
		w.WriteString("Task checks (`at verify task`):\n")
		for _, ch := range v.Checks {
			req := "required"
			if !ch.Required {
				req = "optional"
			}
			fmt.Fprintf(&w, "- %s (%s): `%s`\n", ch.ID, req, shellJoin(ch.Command))
		}
		w.WriteString("\n")
	}
	w.WriteString("Regression checks (`at verify regression`):\n")
	for _, ch := range c.Regression {
		fmt.Fprintf(&w, "- %s: `%s`\n", ch.ID, shellJoin(ch.Command))
	}
	w.WriteString("\n")
	if len(v.Requires) > 0 {
		w.WriteString("Prerequisites (complete; their work is on the target branch):\n")
		for _, r := range v.Requires {
			fmt.Fprintf(&w, "- %s %s\n", r.ID, r.Title)
		}
		w.WriteString("\n")
	}
	if len(c.Siblings) > 0 {
		w.WriteString("Other open tasks (other workers' work; not yours to implement):\n")
		for _, s := range c.Siblings {
			fmt.Fprintf(&w, "- %s %s [%s]\n", s.ID, s.Title, s.Status)
		}
		w.WriteString("\n")
	}
	if c.Workspace != "" {
		fmt.Fprintf(&w, "Workspace: %s (branch %s; completion promotes it to %s).", c.Workspace, c.Branch, c.TargetBranch)
		if c.LeaseUntil != nil {
			fmt.Fprintf(&w, " Lease until %s.", c.LeaseUntil.UTC().Format(time.RFC3339))
		}
		w.WriteString("\n")
		if !retry {
			renderState(&w, c)
		}
	} else {
		fmt.Fprintf(&w, "Completion promotes the task branch to %s. ", c.TargetBranch)
		switch v.Status {
		case task.StatusComplete:
			fmt.Fprintf(&w, "This task is complete (revision %s); nothing is left to do.\n", short(v.CompletedRevision))
		case task.StatusReady, task.StatusInterrupted, task.StatusVerificationFailed:
			fmt.Fprintf(&w, "Nobody holds this task; `at claim %s` gives you a workspace.\n", v.ID)
		default:
			fmt.Fprintf(&w, "This task is %s and cannot be claimed right now.\n", v.Status)
		}
		if !retry {
			renderState(&w, c)
		}
	}
	if c.Worktrunk {
		fmt.Fprintf(&w, "Commit with `wt step commit` (Worktrunk is installed: `wt step diff` shows all changes since branching, `wt step squash` folds commits; on INTEGRATION_FAILED use `wt step rebase %s`).\n", c.TargetBranch)
	} else {
		fmt.Fprintf(&w, "Commit with git; on INTEGRATION_FAILED use `git merge %s`.\n", c.TargetBranch)
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
		fmt.Fprintf(w, "Last `at verify %s`: %s\n", f.Mode, f.Summary)
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
		}
	}
}

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

// Rules is the agent execution contract, the same text as the
// "Agent execution contract" section of AGENTS.md (a test keeps them in
// sync), for hosts whose agent does not read that file.
const Rules = `## Agent execution contract

Complete one independently verifiable task at a time.

Start

- Run ` + "`at context`" + ` before working. Review the handoff, the existing changes and ` + "`git status`" + ` before editing; preserve useful prior work.
- Work only in the assigned workspace. The session token is stored there, so ` + "`at`" + ` commands run there need no token.
- Do not implement other tasks.

Execute

- Implement the outcome and satisfy its acceptance criteria.
- The checks are the contract: never weaken, skip or bypass a check or the tests it runs.
- Iterate with ` + "`at verify task`" + ` and ` + "`at verify regression`" + ` (diagnostic; they never complete anything).
- Record progress with ` + "`at log --done \"...\" --next \"...\" --learned \"...\"`" + `; ` + "`--next`" + ` is what the next attempt reads first.
- The lease lasts 30 minutes and every ` + "`at`" + ` command renews it; log before it runs out.
- On LEASE_EXPIRED or SESSION_SUPERSEDED, stop editing immediately. The next ` + "`at claim <task>`" + ` returns your handoff.

Complete

- Commit before ` + "`at verify complete`" + ` (` + "`wt step commit`" + ` where Worktrunk is installed, plain git otherwise). The tree must be clean; nothing is committed for you.
- You never set status. Only a passing ` + "`at verify complete`" + ` completes the task: it runs every required task check and regression check fresh on the committed revision, then promotes the branch to the target. When the target had moved it merges for you and reports ` + "`integrated_revision`" + `.
- On VERIFICATION_FAILED, read ` + "`error.details`" + `, fix the cause, commit, verify again. Your claim stays live.
- On INTEGRATION_FAILED, the target moved against you: ` + "`wt step rebase <target>`" + ` (or ` + "`git merge <target>`" + `), resolve ` + "`error.details.conflicts`" + `, commit, verify again. It is not counted against you.
- If completion depends on work that is not yours: ` + "`at add \"prereq\" --blocks <task> --check \"id: cmd\"`" + `, ` + "`at log`" + ` the handoff, ` + "`at claim release`" + `. Do not implement the blocking task.

Exit

- ` + "`at claim --wait`" + ` ends with DONE (everything is complete) or STALLED (a planner is needed); both are normal exits for a worker loop.
- Never claim success without a passing final verification.
`
