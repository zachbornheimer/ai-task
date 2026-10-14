package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

// DoctorCheck is one finding of Doctor. Severity "error" means the
// project cannot be worked safely until it is fixed; "warning" means a
// planner should look; "info" is context.
type DoctorCheck struct {
	ID       string `json:"id"`
	OK       bool   `json:"ok"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Remedy   string `json:"remedy,omitempty"`
	// Evidence carries the failing baseline checks' outcomes and output
	// tails when the baseline is unhealthy.
	Evidence []verification.Evidence `json:"evidence,omitempty"`
}

// DoctorReport is the project health report. Healthy is false when any
// error-severity check failed.
type DoctorReport struct {
	Project        project.ID    `json:"project"`
	Root           string        `json:"root"`
	Target         string        `json:"target_branch"`
	TargetRevision string        `json:"target_revision,omitempty"`
	Healthy        bool          `json:"healthy"`
	Checks         []DoctorCheck `json:"checks"`
	Summary        *Summary      `json:"summary,omitempty"`
}

// Doctor inspects a project the way a planner should before adding tasks
// and a host should before starting workers: the repository and target
// branch resolve, a Git identity exists for the agents' commits, the
// regression suite is defined and passes on the target branch, the
// workspace root is writable, and no task is waiting on a planner.
// Nothing is written to the database or the repository.
func (e *Engine) Doctor(ctx context.Context, pid project.ID) (DoctorReport, error) {
	p, err := e.Project(ctx, pid)
	if err != nil {
		return DoctorReport{}, err
	}
	rep := DoctorReport{Project: p.ID, Root: p.RootPath, Target: p.TargetBranch, Healthy: true}
	add := func(id string, ok bool, severity, msg, remedy string) *DoctorCheck {
		rep.Checks = append(rep.Checks, DoctorCheck{ID: id, OK: ok, Severity: severity, Message: msg, Remedy: remedy})
		if !ok && severity == "error" {
			rep.Healthy = false
		}
		return &rep.Checks[len(rep.Checks)-1]
	}
	mgr := e.manager(p)

	// Repository.
	if st, err := os.Stat(p.RootPath); err != nil || !st.IsDir() {
		add("repository", false, "error", fmt.Sprintf("project root %s is not a directory", p.RootPath), "restore the checkout or re-register the project with `atask init --path`")
		return rep, nil
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", p.RootPath, "rev-parse", "--is-inside-work-tree").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "true" {
		add("repository", false, "error", fmt.Sprintf("%s is not a Git work tree", p.RootPath), "run `git init` there or point the project at the repository")
		return rep, nil
	}
	add("repository", true, "info", fmt.Sprintf("Git repository at %s", p.RootPath), "")

	if _, err := exec.LookPath("wt"); err != nil {
		add("worktrunk", false, "error", "Worktrunk (wt) is not on PATH; task worktrees cannot be created", "install Worktrunk from https://worktrunk.dev and put `wt` on PATH")
	} else {
		add("worktrunk", true, "info", "Worktrunk (wt) is on PATH; task worktrees follow worktree-path", "")
	}

	// Target branch.
	rev, err := mgr.Revision(ctx, "refs/heads/"+p.TargetBranch)
	if err != nil {
		add("target_branch", false, "error", fmt.Sprintf("target branch %s does not exist", p.TargetBranch), fmt.Sprintf("create it (`git branch %s`) or set another with `atask project --target-branch`", p.TargetBranch))
	} else {
		rep.TargetRevision = rev
		add("target_branch", true, "info", fmt.Sprintf("target %s at %s", p.TargetBranch, short(rev)), "")
	}

	// Uncommitted work in the target checkout is invisible to the
	// baseline and to every task worktree, which start from the commit.
	if files, err := workspace.UncommittedFiles(ctx, p.RootPath); err == nil {
		if len(files) > 0 {
			add("uncommitted_files", false, "warning", fmt.Sprintf("the target checkout has %d uncommitted or untracked file(s): %s; checks and task worktrees run on the committed revision and do not see them", len(files), listFiles(files, 5)), fmt.Sprintf("commit them (git -C %s add -A && git -C %s commit) or remove them", p.RootPath, p.RootPath))
		} else {
			add("uncommitted_files", true, "info", "the target checkout is clean", "")
		}
	}

	// Git identity: agents commit in the task worktrees, and at records
	// integration merges as the same identity; Git refuses both without one.
	if ident, err := workspace.Identity(ctx, p.RootPath); err != nil {
		add("git_identity", false, "error", "no Git identity for the repository: agents cannot commit and at cannot record the merges that build a dependent's base", fmt.Sprintf("git -C %s config user.name <name> && git -C %s config user.email <email>", p.RootPath, p.RootPath))
	} else {
		add("git_identity", true, "info", "commits and merges as "+ident, "")
	}

	// Workspace root writable.
	root := mgr.Root
	if err := os.MkdirAll(root, 0o755); err != nil {
		add("workspace_root", false, "error", fmt.Sprintf("cannot create workspace root %s: %v", root, err), "make the directory writable or set the project's workspace root")
	} else if f, err := os.CreateTemp(root, ".doctor-*"); err != nil {
		add("workspace_root", false, "error", fmt.Sprintf("workspace root %s is not writable: %v", root, err), "fix the permissions on the directory")
	} else {
		discard.CloseReadOnly(f) // probe file is empty; nothing was written
		_ = os.Remove(f.Name())  // best-effort cleanup of the probe file
		add("workspace_root", true, "info", fmt.Sprintf("worktrees under %s", root), "")
	}

	// Stale quarantined worktrees.
	if stale, _ := filepath.Glob(filepath.Join(root, "*.stale-*")); len(stale) > 0 {
		add("stale_workspaces", false, "warning", fmt.Sprintf("%d quarantined worktree(s) from attempts that never ended", len(stale)), "atask prune")
	} else {
		add("stale_workspaces", true, "info", "no quarantined worktrees", "")
	}

	// Plan health.
	sum, err := e.Summary(ctx, pid)
	if err != nil {
		return rep, err
	}
	rep.Summary = &sum
	if n := sum.Counts[task.StatusNeedsAttention]; n > 0 {
		snap, err := e.List(ctx, ListQuery{ProjectID: pid, Filter: FilterOpen})
		if err != nil {
			return rep, err
		}
		var ids []string
		for _, t := range snap.Tasks {
			if t.Status == task.StatusNeedsAttention {
				ids = append(ids, fmt.Sprintf("%s (%s)", t.ID, t.AttentionReason))
			}
		}
		add("needs_attention", false, "warning", fmt.Sprintf("%d task(s) need a planner: %s", n, strings.Join(ids, "; ")), "atask update <task> --reset-attempts, or atask claim <task> to retry its worktree")
	} else {
		add("needs_attention", true, "info", "no task needs attention", "")
	}
	switch {
	case sum.Done:
		add("plan", true, "info", "every task is complete", "")
	case sum.Stalled:
		add("plan", false, "warning", "the plan is stalled: "+strings.Join(sum.Reasons, "; "), "add, update or archive tasks so something becomes claimable")
	default:
		add("plan", true, "info", fmt.Sprintf("%d open task(s), %d claimable, %d active", sum.Open, sum.Claimable, sum.Active), "")
	}
	return rep, nil
}

// listFiles joins up to max names and counts the rest.
func listFiles(files []string, max int) string {
	if len(files) <= max {
		return strings.Join(files, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(files[:max], ", "), len(files)-max)
}

func budgetLabel(d time.Duration) string {
	if d <= 0 {
		return "unlimited"
	}
	return d.String()
}
