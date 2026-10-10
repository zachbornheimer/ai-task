package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// Brief is the prompt a host hands a coding agent for one claimed task,
// plus the facts it was built from. The text is the same for every
// harness: the agent only needs a shell with `at` on PATH.
type Brief struct {
	Prompt       string                   `json:"prompt"`
	Project      project.ID               `json:"project"`
	Task         *TaskView                `json:"task,omitempty"`
	Workspace    string                   `json:"workspace,omitempty"`
	Branch       string                   `json:"branch,omitempty"`
	TargetBranch string                   `json:"target_branch"`
	Regression   []verification.CheckSpec `json:"regression_checks"`
}

// Brief builds the agent prompt. With a token it describes that session's
// task (contract, checks, handoff, workspace); without one it is the
// generic worker brief for the project.
func (e *Engine) Brief(ctx context.Context, pid project.ID, token execution.Token) (Brief, error) {
	var view *TaskView
	if token != "" {
		v, err := e.Whoami(ctx, token)
		if err != nil {
			return Brief{}, err
		}
		if pid == "" {
			if p, err := e.ResolveProject(ctx, v.Project, ""); err == nil {
				pid = p.ID
			}
		}
		full, err := e.Show(ctx, pid, string(v.ID), true)
		if err == nil {
			v = full
		}
		view = &v
	}
	p, err := e.Project(ctx, pid)
	if err != nil {
		return Brief{}, err
	}
	b := Brief{Project: p.ID, Task: view, TargetBranch: p.TargetBranch, Regression: p.Regression}
	if view != nil && view.Attempt != nil {
		b.Workspace, b.Branch = view.Attempt.Workspace, view.Attempt.Branch
	}
	b.Prompt = renderBrief(p, b)
	return b, nil
}

func renderBrief(p project.Project, b Brief) string {
	var w strings.Builder
	v := b.Task
	if v != nil {
		fmt.Fprintf(&w, "# Task %s: %s\n\n", v.ID, v.Title)
		if v.Outcome != "" && v.Outcome != v.Title {
			fmt.Fprintf(&w, "Outcome: %s\n\n", v.Outcome)
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
		if len(v.Checks) > 0 {
			w.WriteString("Task checks (`at verify task`; all required ones must pass; read the commands, they are the contract):\n")
			for _, c := range v.Checks {
				req := "required"
				if !c.Required {
					req = "optional"
				}
				fmt.Fprintf(&w, "- %s (%s): %s\n", c.ID, req, strings.Join(c.Command, " "))
			}
			w.WriteString("\n")
		}
		if len(v.Requires) > 0 {
			w.WriteString("Prerequisites (complete; their work is already on your branch):\n")
			for _, r := range v.Requires {
				fmt.Fprintf(&w, "- %s %s\n", r.ID, r.Title)
			}
			w.WriteString("\n")
		}
		if b.Workspace != "" {
			fmt.Fprintf(&w, "Workspace: %s (branch %s). Work only there; the session token is stored in it, so `at` commands run there need no token.\n", b.Workspace, b.Branch)
		}
		if v.Attempt != nil && v.Attempt.Seq > 1 {
			fmt.Fprintf(&w, "This is attempt %d: read the handoff below before touching the code.\n", v.Attempt.Seq)
		}
		w.WriteString("\n")
		if v.Handoff != nil && !v.Handoff.Empty() {
			w.WriteString("## Handoff from earlier attempts\n\n")
			h := v.Handoff
			if h.LatestNext != "" {
				fmt.Fprintf(&w, "Next step recorded last time: %s\n", h.LatestNext)
			}
			for _, l := range h.Learnings {
				fmt.Fprintf(&w, "- learned: %s\n", l)
			}
			for _, l := range h.Inherited {
				fmt.Fprintf(&w, "- learned on prerequisite %s: %s\n", l.TaskID, l.Learned)
			}
			if h.LastFailure != nil {
				fmt.Fprintf(&w, "Last failed verification (%s): %s\n", h.LastFailure.Mode, h.LastFailure.Summary)
				for _, c := range h.LastFailure.Checks {
					fmt.Fprintf(&w, "- %s %s", c.CheckID, c.Outcome)
					if c.Message != "" {
						fmt.Fprintf(&w, ": %s", c.Message)
					}
					w.WriteString("\n")
					if t := strings.TrimSpace(c.StderrTail); t != "" {
						fmt.Fprintf(&w, "  stderr: %s\n", strings.ReplaceAll(t, "\n", "\n  "))
					}
				}
			}
			w.WriteString("\n")
		}
	} else {
		fmt.Fprintf(&w, "# Worker brief for project %s\n\n", p.Name)
		w.WriteString("Claim a task with `at claim --wait` (from the project root), `cd` into the `workspace` it prints, and work there. DONE means every task is complete; STALLED means a planner is needed; both end the loop.\n\n")
	}
	fmt.Fprintf(&w, "Project regression checks (`at verify regression`; `at verify complete` runs them after the task checks):\n")
	for _, c := range p.Regression {
		fmt.Fprintf(&w, "- %s: %s\n", c.ID, strings.Join(c.Command, " "))
	}
	fmt.Fprintf(&w, "\nVerified work is promoted to branch %s.\n\n", p.TargetBranch)
	w.WriteString(`## How to work

1. Implement the outcome in the workspace. Keep edits to what the task needs.
2. Record progress as you go: ` + "`at log --done \"...\" --next \"...\" --learned \"...\"`" + ` (at least every 30 minutes; every at command renews your lease).
3. Iterate with ` + "`at verify task`" + ` and ` + "`at verify regression`" + ` (diagnostic, run in your working tree).
4. Commit everything (the tree must be clean; nothing is committed for you), then run ` + "`at verify complete`" + `. It runs both suites fresh on your committed revision, promotes your branch to the target, and completes the task. You never set status yourself.
5. On VERIFICATION_FAILED read error.details, fix, commit, verify again. On INTEGRATION_FAILED the target moved against you: ` + "`git merge <target>`" + ` in the workspace, resolve error.details.conflicts, commit, verify again (not counted against you).
6. If the task needs work that is not yours: ` + "`at add \"prereq\" --blocks <task> --check \"id: cmd\"`" + `, ` + "`at log`" + ` the handoff, ` + "`at claim release`" + `.
7. LEASE_EXPIRED or SESSION_SUPERSEDED: stop editing immediately.

Use AT_OUTPUT=json for machine-readable results. Do not edit the at database, the check commands, or files outside the workspace.
`)
	return w.String()
}
