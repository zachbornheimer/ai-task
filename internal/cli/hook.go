package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

func init() {
	register("hook", "host adapter (not an agent verb): atask hook session-start | guard-edit | stop", runHook)
}

// hookInput is the subset of Claude Code's hook JSON the hooks read.
type hookInput struct {
	Cwd            string `json:"cwd"`
	ToolName       string `json:"tool_name"`
	StopHookActive bool   `json:"stop_hook_active"`
	ToolInput      struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"` // NotebookEdit
	} `json:"tool_input"`
}

// runHook answers Claude Code hooks. Each writes Claude Code's JSON
// decision form on stdout and exits 0; an `atask` failure never blocks the
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

// hookSessionStart injects the current claim when one exists. It does
// not dump the board. With no claim it writes nothing.
func (c *ctxt) hookSessionStart(ctx context.Context, cwd string) error {
	e, err := c.engine(ctx)
	if err != nil {
		return nil
	}
	tok := workspace.LoadToken(ctx, cwd)
	if tok == "" {
		tok = c.env.Getenv("ATASK_SESSION")
	}
	if tok == "" {
		return nil
	}
	token, err := execution.ParseToken(tok)
	if err != nil {
		return nil
	}
	v, err := e.Whoami(ctx, token)
	if err != nil {
		return nil
	}
	return c.emitSessionStart(fmt.Sprintf("# Task %s: %s", v.ID, v.Title))
}

func (c *ctxt) emitSessionStart(context string) error {
	context = strings.TrimSpace(context)
	if context == "" {
		return nil
	}
	return c.encoder().Encode(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": context,
		},
	})
}

// hookGuardEdit denies an edit to a file inside a registered project that
// is not in a task worktree (branch atask/<id>): work goes through a claim.
// Files outside any registered project, and sessions with
// ATASK_ALLOW_DIRECT=1, are left alone.
func (c *ctxt) hookGuardEdit(ctx context.Context, cwd string, in hookInput) error {
	path := in.ToolInput.FilePath
	if path == "" {
		path = in.ToolInput.NotebookPath
	}
	if path == "" || c.env.Getenv("ATASK_ALLOW_DIRECT") == "1" {
		return nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	dir := nearestExistingDir(filepath.Dir(path))
	top, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return nil // not in a repository
	}
	branch := workspace.CurrentBranch(ctx, dir)
	if id := strings.TrimPrefix(branch, "atask/"); id != branch && task.IsID(id) {
		return nil // inside a task worktree
	}
	e, err := c.engine(ctx)
	if err != nil {
		return nil
	}
	p, err := e.ResolveProject(ctx, "", strings.TrimSpace(string(top)))
	if err != nil {
		return nil // not an atask project
	}
	reason := fmt.Sprintf("%s is on branch %q of project %s, not in a task worktree. Work goes through atask: `atask list` to see the plan, `atask claim <task>` (or `atask add \"...\" --proof \"run: ...\" --regression \"run: ...\" --claim` for a new change), then edit inside the workspace the claim prints. This guard only sees Edit/Write/NotebookEdit; shell and other writers are not blocked. Set ATASK_ALLOW_DIRECT=1 to bypass deliberately.", path, branch, p.Name)
	return c.encoder().Encode(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason}})
}

// nearestExistingDir walks up from dir to the closest directory that exists,
// so a file about to be created in a new directory is judged by the
// repository that directory would belong to.
func nearestExistingDir(dir string) string {
	for {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

// hookStop is a no-op. An open claim is not a reason to block ending a
// turn: an orchestrator may delegate and return while a worker holds a
// lease. The command exists so a host that still registers the hook
// does not fail.
func (c *ctxt) hookStop(context.Context, string, hookInput) error {
	return nil
}
