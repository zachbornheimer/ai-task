package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

func init() {
	register("hook", "answer a Claude Code hook (JSON on stdin): at hook session-start | guard-edit | stop", runHook)
}

// hookInput is the subset of Claude Code's hook JSON the hooks read.
type hookInput struct {
	Cwd            string `json:"cwd"`
	ToolName       string `json:"tool_name"`
	StopHookActive bool   `json:"stop_hook_active"`
	ToolInput      struct {
		FilePath string `json:"file_path"`
	} `json:"tool_input"`
}

// runHook answers Claude Code hooks. Each writes Claude Code's JSON
// decision form on stdout and exits 0; an `at` failure never blocks the
// harness (the hooks are a safety net, not a gate the engine depends on).
func runHook(ctx context.Context, c *ctxt, args []string) error {
	if err := c.parse(args); err != nil {
		return err
	}
	if len(c.args) != 1 {
		return usage("hook needs one event: session-start, guard-edit or stop")
	}
	var in hookInput
	if b, err := io.ReadAll(c.env.Stdin); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		_ = json.Unmarshal(b, &in)
	}
	cwd := in.Cwd
	if cwd == "" {
		cwd = c.env.Cwd
	}
	switch c.args[0] {
	case "session-start":
		return c.hookSessionStart(ctx, cwd)
	case "guard-edit":
		return c.hookGuardEdit(ctx, cwd, in)
	case "stop":
		return c.hookStop(ctx, cwd, in)
	}
	return usage("unknown hook event %q", c.args[0])
}

// hookSessionStart prints the plan or task context so the session starts
// knowing the state of the work. Nothing to say when `at` is not in use.
func (c *ctxt) hookSessionStart(ctx context.Context, cwd string) error {
	e, err := c.engine(ctx)
	if err != nil {
		return nil
	}
	tok := workspace.LoadToken(ctx, cwd)
	if tok == "" {
		tok = c.env.Getenv("AT_SESSION")
	}
	var token execution.Token
	if tok != "" {
		token, _ = execution.ParseToken(tok)
	}
	p, perr := e.ResolveProject(ctx, c.g.project, cwd)
	if token == "" && perr != nil {
		return nil
	}
	pid := p.ID
	cx, err := e.Context(ctx, pid, "", token, false)
	if err != nil {
		return nil
	}
	fmt.Fprintln(c.env.Stdout, cx.Prompt)
	return nil
}

// hookGuardEdit denies an edit to a file inside a registered project that
// is not in a task worktree (branch at/<id>): work goes through a claim.
// Files outside any registered project, and sessions with
// AT_ALLOW_DIRECT=1, are left alone.
func (c *ctxt) hookGuardEdit(ctx context.Context, cwd string, in hookInput) error {
	path := in.ToolInput.FilePath
	if path == "" || c.env.Getenv("AT_ALLOW_DIRECT") == "1" {
		return nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	dir := filepath.Dir(path)
	top, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return nil // not in a repository
	}
	branch := workspace.CurrentBranch(ctx, dir)
	if id := strings.TrimPrefix(branch, "at/"); id != branch && task.IsID(id) {
		return nil // inside a task worktree
	}
	e, err := c.engine(ctx)
	if err != nil {
		return nil
	}
	p, err := e.ResolveProject(ctx, "", strings.TrimSpace(string(top)))
	if err != nil {
		return nil // not an at project
	}
	reason := fmt.Sprintf("%s is on branch %q of project %s, not in a task worktree. Work goes through at: `at context` to see the plan, `at claim <task>` (or `at add \"...\" --check \"...\" --claim` for a new change), then edit inside the workspace the claim prints. Set AT_ALLOW_DIRECT=1 to bypass deliberately.", path, branch, p.Name)
	return c.encoder().Encode(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason}})
}

// hookStop refuses to end a turn while the session holds a task that is
// neither verified nor released: the agent is told what to run. The
// task is the one whose worktree the session is in (or AT_SESSION);
// stop_hook_active ends the loop as Claude Code documents.
func (c *ctxt) hookStop(ctx context.Context, cwd string, in hookInput) error {
	if in.StopHookActive {
		return nil
	}
	tok := workspace.LoadToken(ctx, cwd)
	if tok == "" {
		tok = c.env.Getenv("AT_SESSION")
	}
	if tok == "" {
		return nil
	}
	token, err := execution.ParseToken(tok)
	if err != nil {
		return nil
	}
	e, err := c.engine(ctx)
	if err != nil {
		return nil
	}
	v, err := e.Whoami(ctx, token)
	if err != nil || v.Attempt == nil || v.Attempt.EndedAt != nil || !v.Attempt.LeaseActive {
		return nil
	}
	reason := fmt.Sprintf("You still hold %s (%s). Finish it: commit and run `at verify complete`; or hand it back with `at log --next \"...\"` and `at claim release --note \"...\"`.", v.ID, v.Title)
	return c.encoder().Encode(map[string]any{"decision": "block", "reason": reason})
}
