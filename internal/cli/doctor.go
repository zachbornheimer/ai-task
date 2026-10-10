package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
)

func init() {
	register("doctor", "check the project before planning or starting workers: repository, target branch, Git identity, regression suite passing on the target, workspace root, stale worktrees, tasks needing attention [--skip-baseline]", runDoctor)
}

func runDoctor(ctx context.Context, c *ctxt, args []string) error {
	skip := c.fs.Bool("skip-baseline", false, "do not run the regression checks on the target branch")
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
	rep, err := e.Doctor(ctx, pid, app.DoctorOptions{SkipBaseline: *skip})
	if err != nil {
		return err
	}
	rep.Checks = append(rep.Checks, worktrunkChecks(ctx, rep.Root)...)
	if !rep.Healthy {
		return &fault.Error{Code: fault.CodeUnhealthy, Message: "the project is not ready to be worked; see the report", Details: rep}
	}
	return c.emit(rep, func(w io.Writer) { renderDoctor(w, rep) })
}

func renderDoctor(w io.Writer, rep app.DoctorReport) {
	fmt.Fprintf(w, "Project %s · %s (target %s)\n", rep.Project, rep.Root, rep.Target)
	for _, ch := range rep.Checks {
		mark := "✓"
		switch {
		case !ch.OK && ch.Severity == "error":
			mark = "✗"
		case !ch.OK:
			mark = "!"
		}
		fmt.Fprintf(w, "%s %-18s %s\n", mark, ch.ID, ch.Message)
		if !ch.OK && ch.Remedy != "" {
			fmt.Fprintf(w, "    fix: %s\n", ch.Remedy)
		}
		for _, ev := range ch.Evidence {
			fmt.Fprintf(w, "    %s %s exit=%d\n", ev.CheckID, ev.Outcome, ev.ExitCode)
			if t := strings.TrimSpace(ev.Stderr); t != "" {
				fmt.Fprintf(w, "      %s\n", strings.ReplaceAll(tail(t, 400), "\n", "\n      "))
			}
		}
	}
	if rep.Healthy {
		fmt.Fprintln(w, "● healthy")
	} else {
		fmt.Fprintln(w, "○ not ready: fix the ✗ items")
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// worktrunkChecks reports how Worktrunk is set up, when it is installed:
// commit-message generation (else `wt step commit` writes a fallback
// message) and project hooks that need a one-time approval before a
// headless agent can run them.
func worktrunkChecks(ctx context.Context, root string) []app.DoctorCheck {
	if _, err := exec.LookPath("wt"); err != nil {
		return nil
	}
	var out []app.DoctorCheck
	home, _ := os.UserHomeDir()
	cfg, _ := os.ReadFile(filepath.Join(home, ".config", "worktrunk", "config.toml"))
	if strings.Contains(string(cfg), "[commit.generation]") {
		out = append(out, app.DoctorCheck{ID: "worktrunk_commits", OK: true, Severity: "info", Message: "wt step commit generates commit messages"})
	} else {
		out = append(out, app.DoctorCheck{ID: "worktrunk_commits", OK: false, Severity: "warning", Message: "Worktrunk is installed but [commit.generation] is not configured: `wt step commit` will write fallback messages such as \"Changes to x.go\"", Remedy: "wt config create, then set [commit.generation] (see `wt config --help`)"})
	}
	if _, err := os.Stat(filepath.Join(root, ".config", "wt.toml")); err == nil {
		out = append(out, app.DoctorCheck{ID: "worktrunk_hooks", OK: true, Severity: "info", Message: ".config/wt.toml defines Worktrunk hooks or aliases; a headless agent can run them only after `wt config approvals add` was run once by a person"})
	}
	return out
}
