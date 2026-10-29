package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
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

// DoctorReport is the health report for one repository and the project's
// plan. Healthy is false when any error-severity check failed.
type DoctorReport struct {
	Project        project.ID    `json:"project"`
	Repo           string        `json:"repo,omitempty"`
	BaseRef        string        `json:"base_ref,omitempty"`
	TargetRevision string        `json:"base_revision,omitempty"`
	Healthy        bool          `json:"healthy"`
	Checks         []DoctorCheck `json:"checks"`
	Summary        *Summary      `json:"summary,omitempty"`
}

// Doctor inspects a repository the way a worker should before claiming work
// in it: the checkout is a Git work tree, its base branch resolves, a Git
// identity exists for the agents' commits, the workspace root is writable,
// and no task of the project is waiting on a planner. An empty repo checks
// only the plan. Nothing is written to the database or the repository.
func (e *Engine) Doctor(ctx context.Context, pid project.ID, repo string) (DoctorReport, error) {
	p, err := e.Project(ctx, pid)
	if err != nil {
		return DoctorReport{}, err
	}
	rep := DoctorReport{Project: p.ID, Repo: repo, Healthy: true}
	add := func(id string, ok bool, severity, msg, remedy string) {
		rep.Checks = append(rep.Checks, DoctorCheck{ID: id, OK: ok, Severity: severity, Message: msg, Remedy: remedy})
		if !ok && severity == "error" {
			rep.Healthy = false
		}
	}

	// Repository and its base branch.
	if repo == "" {
		add("repository", false, "warning", "no repository here: tasks filed from this directory run their checks in place", "run `atask doctor` inside a Git work tree, or `atask add --repo <path>` to file work against one")
	} else if !isDirectory(repo) {
		add("repository", false, "error", fmt.Sprintf("repository root %s is not a directory", repo), "restore the checkout")
		return rep, nil
	} else if !workspace.InsideWorkTree(ctx, repo) {
		add("repository", false, "error", fmt.Sprintf("%s is not a Git work tree", repo), "run `git init` there")
		return rep, nil
	} else {
		add("repository", true, "info", fmt.Sprintf("Git repository at %s", repo), "")
	}

	if _, err := exec.LookPath("wt"); err != nil {
		add("worktrunk", false, "error", "Worktrunk (wt) is not on PATH; task worktrees cannot be created", "install Worktrunk from https://worktrunk.dev and put `wt` on PATH")
	} else {
		add("worktrunk", true, "info", "Worktrunk (wt) is on PATH; task worktrees follow worktree-path", "")
	}

	if repo != "" {
		rep.BaseRef = BaseRef(ctx, repo)
		rev, err := e.manager(repo).Revision(ctx, "refs/heads/"+rep.BaseRef)
		if err != nil {
			add("base_ref", false, "error", fmt.Sprintf("base branch %s does not exist", rep.BaseRef), fmt.Sprintf("create it (`git branch %s`) or check out the repository's default branch", rep.BaseRef))
		} else {
			rep.TargetRevision = rev
			add("base_ref", true, "info", fmt.Sprintf("base %s at %s", rep.BaseRef, short(rev)), "")
		}

		// Uncommitted work in the base checkout is invisible to the
		// baseline and to every task worktree, which start from the commit.
		if files, err := workspace.UncommittedFiles(ctx, repo); err == nil {
			if len(files) > 0 {
				add("uncommitted_files", false, "warning", fmt.Sprintf("the base checkout has %d uncommitted or untracked file(s): %s; checks and task worktrees run on the committed revision and do not see them", len(files), listFiles(files, 5)), fmt.Sprintf("commit them (git -C %s add -A && git -C %s commit) or remove them", repo, repo))
			} else {
				add("uncommitted_files", true, "info", "the base checkout is clean", "")
			}
		}

		// Git identity: agents commit in the task worktrees, and at records
		// integration merges as the same identity; Git refuses both without one.
		if ident, err := workspace.Identity(ctx, repo); err != nil {
			add("git_identity", false, "error", "no Git identity for the repository: agents cannot commit and at cannot record the merges that build a dependent's base", fmt.Sprintf("git -C %s config user.name <name> && git -C %s config user.email <email>", repo, repo))
		} else {
			add("git_identity", true, "info", "commits and merges as "+ident, "")
		}
	}

	// Workspace root writable.
	root := e.wsRoot
	if err := os.MkdirAll(root, 0o750); err != nil {
		add("workspace_root", false, "error", fmt.Sprintf("cannot create workspace root %s: %v", root, err), "make the directory writable or set the engine's workspace root")
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
		for i := range snap.Tasks {
			t := &snap.Tasks[i]
			if t.Status == task.StatusNeedsAttention {
				ids = append(ids, fmt.Sprintf("%s (%s)", t.ID, t.AttentionReason))
			}
		}
		add("needs_attention", false, "warning", fmt.Sprintf("%d task(s) need a planner: %s", n, strings.Join(ids, "; ")), "atask update <task> --reset-attempts, or atask claim <task> to retry its worktree")
	} else {
		add("needs_attention", true, "info", "no task needs attention", "")
	}
	var abandoned []sqlite.AbandonedRun
	if err := e.store.Read(ctx, func(tx *sqlite.Tx) (err error) { abandoned, err = tx.AbandonedRuns(pid); return err }); err != nil {
		return rep, err
	}
	if len(abandoned) > 0 {
		found := make([]string, len(abandoned))
		for i, a := range abandoned {
			found[i] = fmt.Sprintf("%s run %d (%s)", a.TaskID, a.RunID, strings.TrimPrefix(a.Summary, sqlite.AbandonedPrefix+" "))
		}
		add("abandoned_runs", false, "warning", fmt.Sprintf("%d task(s) had a verification run abandoned because its process died: %s; abandoned runs are not counted as failures", len(abandoned), strings.Join(found, "; ")), "run `atask verify complete` again, or `atask claim <task>` if the claim was released")
	} else {
		add("abandoned_runs", true, "info", "no verification run was abandoned", "")
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

// listFiles joins up to limit names and counts the rest.
func listFiles(files []string, limit int) string {
	if len(files) <= limit {
		return strings.Join(files, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(files[:limit], ", "), len(files)-limit)
}

// isDirectory reports whether path exists and is a directory.
func isDirectory(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
