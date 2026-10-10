package app

import (
	"context"
	"fmt"
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
	if c.Workspace != "" {
		if st, err := workspace.Inspect(ctx, c.Workspace); err == nil {
			c.DirtyFiles = st.Dirty
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
	w.WriteString("Done when `at verify complete` passes on your committed revision: every required task check, then every regression check, run fresh. The checks are the contract; read them.\n\n")
	if len(v.Checks) > 0 {
		w.WriteString("Task checks (`at verify task`):\n")
		for _, ch := range v.Checks {
			req := "required"
			if !ch.Required {
				req = "optional"
			}
			fmt.Fprintf(&w, "- %s (%s): %s\n", ch.ID, req, strings.Join(ch.Command, " "))
		}
		w.WriteString("\n")
	}
	w.WriteString("Regression checks (`at verify regression`):\n")
	for _, ch := range c.Regression {
		fmt.Fprintf(&w, "- %s: %s\n", ch.ID, strings.Join(ch.Command, " "))
	}
	w.WriteString("\n")
	if len(v.Requires) > 0 {
		w.WriteString("Prerequisites (complete; their work is already on the target branch):\n")
		for _, r := range v.Requires {
			fmt.Fprintf(&w, "- %s %s\n", r.ID, r.Title)
		}
		w.WriteString("\n")
	}
	if len(c.Siblings) > 0 {
		w.WriteString("Other open tasks in this project (not yours: do not implement them; if your task cannot pass without one, add it as a blocker with `at add --blocks` and release):\n")
		for _, s := range c.Siblings {
			fmt.Fprintf(&w, "- %s %s [%s]\n", s.ID, s.Title, s.Status)
		}
		w.WriteString("\n")
	}
	if c.Workspace != "" {
		fmt.Fprintf(&w, "Workspace: %s (branch %s, promoted to %s on completion). Work only there; the session token is stored in it, so `at` commands run there need no token.", c.Workspace, c.Branch, c.TargetBranch)
		if c.LeaseUntil != nil {
			fmt.Fprintf(&w, " Lease until %s; every `at` command renews it.", c.LeaseUntil.UTC().Format(time.RFC3339))
		}
		w.WriteString("\n")
		if len(c.ChangedFiles) > 0 {
			fmt.Fprintf(&w, "Files this branch has already changed: %s\n", strings.Join(c.ChangedFiles, ", "))
		}
		if len(c.DirtyFiles) > 0 {
			fmt.Fprintf(&w, "Uncommitted changes left by an earlier attempt (review with `git status` before editing): %s\n", strings.Join(c.DirtyFiles, ", "))
		}
		w.WriteString("\n")
	} else {
		fmt.Fprintf(&w, "Verified work is promoted to branch %s. ", c.TargetBranch)
		switch v.Status {
		case task.StatusComplete:
			fmt.Fprintf(&w, "This task is complete (revision %s); nothing is left to do.\n\n", short(v.CompletedRevision))
		case task.StatusReady, task.StatusInterrupted, task.StatusVerificationFailed:
			fmt.Fprintf(&w, "Nobody holds this task; `at claim %s` gives you a workspace.\n\n", v.ID)
		default:
			fmt.Fprintf(&w, "This task is %s and cannot be claimed right now.\n\n", v.Status)
		}
	}
	if v.Attempt != nil && v.Attempt.Seq > 1 {
		fmt.Fprintf(&w, "This is attempt %d: read the handoff before touching the code.\n\n", v.Attempt.Seq)
	}
	if h := v.Handoff; h != nil && !h.Empty() {
		w.WriteString("## Handoff from earlier attempts\n\n")
		if h.LatestNext != "" {
			fmt.Fprintf(&w, "Next step recorded last time: %s\n", h.LatestNext)
		}
		for _, l := range h.Learnings {
			fmt.Fprintf(&w, "- learned: %s\n", l)
		}
		for _, l := range h.Inherited {
			fmt.Fprintf(&w, "- learned on prerequisite %s: %s\n", l.TaskID, l.Learned)
		}
		if f := h.LastFailure; f != nil {
			fmt.Fprintf(&w, "Last failed verification (%s): %s\n", f.Mode, f.Summary)
			for _, ch := range f.Checks {
				fmt.Fprintf(&w, "- %s %s", ch.CheckID, ch.Outcome)
				if ch.Message != "" {
					fmt.Fprintf(&w, ": %s", ch.Message)
				}
				w.WriteString("\n")
				for _, tail := range []struct{ name, text string }{{"stdout", ch.StdoutTail}, {"stderr", ch.StderrTail}} {
					if t := strings.TrimSpace(tail.text); t != "" {
						fmt.Fprintf(&w, "  %s: %s\n", tail.name, strings.ReplaceAll(t, "\n", "\n  "))
					}
				}
			}
		}
		w.WriteString("\n")
	}
	w.WriteString("Record progress as you go: `at log --done \"...\" --next \"...\" --learned \"...\"`. Commit, then `at verify complete`.\n")
	return w.String()
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
