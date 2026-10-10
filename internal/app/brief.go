package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
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

// Brief builds the task-specific prompt for a session: the contract,
// checks, regression suite, target branch, workspace and the handoff from
// earlier attempts. The standing rules (AGENTS.md) are appended only when
// withRules is set, for a headless agent that never reads that file.
func (e *Engine) Brief(ctx context.Context, pid project.ID, token execution.Token, withRules bool) (Brief, error) {
	var view *TaskView
	if token != "" {
		// The same enrichment the claim prints: prerequisite learnings
		// and the last failed verification.
		var v TaskView
		err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
			auth, err := tx.AttemptByDigest(token.Digest())
			if err != nil {
				return err
			}
			r, err := tx.GetRecord(auth.Attempt.TaskID)
			if err != nil {
				return err
			}
			proj, err := tx.GetProject(r.Task.ProjectID)
			if err != nil {
				return err
			}
			pid = proj.ID
			if v, err = e.buildView(tx, r, proj, e.now(), true); err != nil {
				return err
			}
			if v.Handoff == nil {
				v.Handoff = &execution.Handoff{}
			}
			return e.inheritContext(tx, r, v.Handoff)
		})
		if err != nil {
			return Brief{}, err
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
	if withRules {
		b.Prompt += "\n" + Rules
	}
	return b, nil
}

// Rules is the standing worker contract, the same text as AGENTS.md's
// rules of the road, for hosts whose agent does not read that file.
const Rules = `## Rules of the road

- You never set status. ` + "`at verify complete`" + ` is the only path to completion and it re-runs everything; VERIFICATION_FAILED leaves your claim live with the evidence in error.details. Fix, commit, verify again.
- Commit before ` + "`at verify complete`" + `; the tree must be clean and nothing is committed for you.
- INTEGRATION_FAILED means your branch no longer merges into the target branch; in the worktree run ` + "`git merge <target>`" + `, resolve error.details.conflicts, commit, verify again. It is not counted against you.
- Your lease is 30 minutes and every at command renews it; ` + "`at log --done .. --next .. --learned ..`" + ` at least that often.
- If the task needs work that is not yours: ` + "`at add \"prereq\" --blocks <task> --check \"id: cmd\"`" + `, log the handoff, ` + "`at claim release`" + `.
- LEASE_EXPIRED or SESSION_SUPERSEDED: stop editing immediately.
- Do not edit the at database, the check commands, or files outside the workspace.
`

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
		fmt.Fprintf(&w, "# Project %s: no task claimed\n\nRun `at claim --wait` from the project root and `cd` into the workspace it prints; `at brief` there describes the task.\n\n", p.Name)
	}
	fmt.Fprintf(&w, "Project regression checks (`at verify regression`; `at verify complete` runs them after the task checks):\n")
	for _, c := range p.Regression {
		fmt.Fprintf(&w, "- %s: %s\n", c.ID, strings.Join(c.Command, " "))
	}
	fmt.Fprintf(&w, "\nVerified work is promoted to branch %s.\n", p.TargetBranch)
	return w.String()
}
