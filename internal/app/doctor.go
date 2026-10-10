package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
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

// DoctorOptions tunes Doctor.
type DoctorOptions struct {
	// SkipBaseline skips running the regression checks on the target
	// branch (the one expensive step).
	SkipBaseline bool
}

// Doctor inspects a project the way a planner should before adding tasks
// and a host should before starting workers: the repository and target
// branch resolve, a Git identity exists for the agents' commits, the
// regression suite is defined and passes on the target branch, the
// workspace root is writable, and no task is waiting on a planner.
// Nothing is written to the database or the repository.
func (e *Engine) Doctor(ctx context.Context, pid project.ID, opts DoctorOptions) (DoctorReport, error) {
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
		add("repository", false, "error", fmt.Sprintf("project root %s is not a directory", p.RootPath), "restore the checkout or re-register the project with `at init --path`")
		return rep, nil
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", p.RootPath, "rev-parse", "--is-inside-work-tree").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "true" {
		add("repository", false, "error", fmt.Sprintf("%s is not a Git work tree", p.RootPath), "run `git init` there or point the project at the repository")
		return rep, nil
	}
	add("repository", true, "info", fmt.Sprintf("Git repository at %s", p.RootPath), "")

	// Target branch.
	rev, err := mgr.Revision(ctx, "refs/heads/"+p.TargetBranch)
	if err != nil {
		add("target_branch", false, "error", fmt.Sprintf("target branch %s does not exist", p.TargetBranch), fmt.Sprintf("create it (`git branch %s`) or set another with `at project --target-branch`", p.TargetBranch))
	} else {
		rep.TargetRevision = rev
		add("target_branch", true, "info", fmt.Sprintf("target %s at %s", p.TargetBranch, short(rev)), "")
	}

	// Git identity: agents commit in the task worktrees and Git refuses a
	// commit without one.
	name, _ := exec.CommandContext(ctx, "git", "-C", p.RootPath, "config", "user.name").Output()
	email, _ := exec.CommandContext(ctx, "git", "-C", p.RootPath, "config", "user.email").Output()
	if strings.TrimSpace(string(name)) == "" || strings.TrimSpace(string(email)) == "" {
		add("git_identity", false, "warning", "no Git user.name/user.email configured for the repository; agents cannot commit until one exists", fmt.Sprintf("git -C %s config user.name <name> && git -C %s config user.email <email>", p.RootPath, p.RootPath))
	} else {
		add("git_identity", true, "info", fmt.Sprintf("commits as %s <%s>", strings.TrimSpace(string(name)), strings.TrimSpace(string(email))), "")
	}

	// Regression suite defined.
	regression := verification.Policy{Regression: p.Regression}
	if err := verification.RequireGate(p.Regression, "regression checks"); err != nil {
		add("regression_checks", false, "error", "the project has no required regression check; no task can be claimed", "at project --regression-check \"test: <command>\"")
	} else {
		add("regression_checks", true, "info", fmt.Sprintf("%d regression check(s)", len(p.Regression)), "")
	}

	// Workspace root writable.
	root := mgr.Root
	if err := os.MkdirAll(root, 0o755); err != nil {
		add("workspace_root", false, "error", fmt.Sprintf("cannot create workspace root %s: %v", root, err), "make the directory writable or set the project's workspace root")
	} else if f, err := os.CreateTemp(root, ".doctor-*"); err != nil {
		add("workspace_root", false, "error", fmt.Sprintf("workspace root %s is not writable: %v", root, err), "fix the permissions on the directory")
	} else {
		f.Close()
		os.Remove(f.Name())
		add("workspace_root", true, "info", fmt.Sprintf("worktrees under %s", root), "")
	}

	// Baseline: the regression suite must pass on the target before any
	// task is claimed, or the first claimant pays for the planner's debt.
	if rep.Healthy && !opts.SkipBaseline && rep.TargetRevision != "" {
		dir, cleanup, err := mgr.Snapshot(ctx, rep.TargetRevision)
		if err != nil {
			add("baseline", false, "error", fmt.Sprintf("cannot snapshot %s: %v", p.TargetBranch, fault.MessageOf(err)), "")
		} else {
			var failed []verification.Evidence
			for _, step := range verification.Plan(regression) {
				ev := e.execStep(ctx, dir, step, 0, rep.TargetRevision, regression.Digest(), false)
				if ev.Required && ev.Outcome != verification.OutcomePassed {
					failed = append(failed, ev)
				}
			}
			cleanup()
			if len(failed) > 0 {
				ids := make([]string, 0, len(failed))
				for _, ev := range failed {
					ids = append(ids, ev.CheckID)
				}
				c := add("baseline", false, "error", fmt.Sprintf("regression checks fail on %s at %s: %s; the first agent to claim would be charged for it", p.TargetBranch, short(rev), strings.Join(ids, ", ")), "make the regression suite pass on the target branch before adding tasks (or fix the checks)")
				c.Evidence = failed
			} else {
				add("baseline", true, "info", fmt.Sprintf("regression checks pass on %s at %s", p.TargetBranch, short(rev)), "")
			}
		}
	}

	// Stale quarantined worktrees.
	if stale, _ := filepath.Glob(filepath.Join(root, "*.stale-*")); len(stale) > 0 {
		add("stale_workspaces", false, "warning", fmt.Sprintf("%d quarantined worktree(s) from attempts that never ended", len(stale)), "at prune")
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
		add("needs_attention", false, "warning", fmt.Sprintf("%d task(s) need a planner: %s", n, strings.Join(ids, "; ")), "at update <task> --reset-attempts --planner, or at claim <task> to retry its worktree")
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
