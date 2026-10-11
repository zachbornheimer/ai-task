package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// Switcher creates or locates a task worktree. Production uses
// Worktrunk so the path follows the user's worktree-path template.
type Switcher interface {
	// Ensure returns the worktree path for branch in repo.
	// When create is true the branch does not exist yet and must be
	// created from base. When create is false the branch exists and a
	// worktree is attached at Worktrunk's template path if needed.
	Ensure(ctx context.Context, repo, branch, base string, create bool) (string, error)
}

// WorktrunkSwitcher shells out to `wt switch`. LookPath and Run are
// seams for tests; nil uses exec.LookPath and exec.CommandContext.
type WorktrunkSwitcher struct {
	LookPath func(string) (string, error)
	Run      func(ctx context.Context, wt, dir string, args []string) ([]byte, error)
}

type switchJSON struct {
	Action string `json:"action"`
	Branch string `json:"branch"`
	Path   string `json:"path"`
}

// Ensure runs:
//
//	wt switch [--create --base BASE] --no-cd --no-hooks --yes --format json BRANCH
func (s WorktrunkSwitcher) Ensure(ctx context.Context, repo, branch, base string, create bool) (string, error) {
	look := s.LookPath
	if look == nil {
		look = exec.LookPath
	}
	wt, err := look("wt")
	if err != nil {
		return "", fault.New(fault.CodeWorkspaceUnavailable, "Worktrunk (wt) is required to create task worktrees: %v", err)
	}
	args := []string{"switch", "--no-cd", "--no-hooks", "--yes", "--format", "json"}
	if create {
		args = append(args, "--create", "--base", base)
	}
	args = append(args, branch)
	run := s.Run
	if run == nil {
		run = runWt
	}
	var out []byte
	delay := 20 * time.Millisecond
	for attempt := range 8 {
		out, err = run(ctx, wt, repo, args)
		if err == nil {
			break
		}
		msg := err.Error() + string(out)
		if attempt == 7 || (!strings.Contains(msg, "lock") && !strings.Contains(msg, "already exists") && !strings.Contains(msg, "busy")) {
			return "", fault.Wrap(err, fault.CodeWorkspaceUnavailable, "wt switch %s: %s", branch, strings.TrimSpace(string(out)))
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
	path, err := parseSwitchPath(out)
	if err != nil {
		return "", err
	}
	return path, nil
}

func runWt(ctx context.Context, wt, dir string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "wt", args...)
	cmd.Path = wt
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func parseSwitchPath(out []byte) (string, error) {
	for line := range bytes.SplitSeq(out, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var s switchJSON
		if err := json.Unmarshal(line, &s); err != nil {
			continue
		}
		if strings.TrimSpace(s.Path) != "" {
			return s.Path, nil
		}
	}
	return "", fault.New(fault.CodeWorkspaceUnavailable, "wt switch --format json did not include path (output %q)", strings.TrimSpace(string(out)))
}
