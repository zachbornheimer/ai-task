package app

import (
	"context"
	"fmt"
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

// Epic verification. A group may carry checks of its own: the integration
// tier, run once on the target branch when every executable member (and
// every verified sub-group) is complete, under the group's size budget
// (large by default). The group is complete only when its checks passed;
// a failure needs a planner, who adds follow-up member tasks (which
// reopen the group) or fixes the check. Group jobs are durable leased
// jobs like cohort jobs: whichever worker has capacity runs them, and a
// verifier that dies is replaced when its lease expires.

// groupJobName namespaces group jobs in the shared jobs table.
func groupJobName(id task.ID) string { return "group:" + string(id) }

// GroupVerification describes where a group with checks stands.
type GroupVerification struct {
	// Status: pending (members done, waiting for a verifier), running,
	// passed, failed, error, or "" (members not done yet).
	Status    string     `json:"status,omitempty"`
	RunID     int64      `json:"run_id,omitempty"`
	Revision  string     `json:"revision,omitempty"`
	Summary   string     `json:"summary,omitempty"`
	At        *time.Time `json:"at,omitempty"`
	NextRetry *time.Time `json:"next_retry,omitempty"`
}

// groupReady reports whether a group's checks may run now: not archived,
// checks defined, at least one executable member, every member complete
// and every sub-group with checks verified.
func (e *Engine) groupReady(tx *sqlite.Tx, rec sqlite.Record, proj project.Project, now time.Time) (bool, error) {
	if rec.Task.Kind != task.KindGroup || rec.Task.Archived() || !verification.HasRequired(rec.Task.Verification.TaskChecks) {
		return false, nil
	}
	prog, err := e.groupProgress(tx, rec.Task.ID, proj, now)
	if err != nil || prog.Total == 0 || prog.Complete != prog.Total {
		return false, err
	}
	children, err := tx.Children(rec.Task.ID)
	if err != nil {
		return false, err
	}
	for _, c := range children {
		if c.Task.Kind == task.KindGroup && verification.HasRequired(c.Task.Verification.TaskChecks) && c.CompletedAt == nil {
			return false, nil
		}
	}
	return true, nil
}

// latestGroupRun is the newest group-mode run of a group, if any.
func latestGroupRun(tx *sqlite.Tx, id task.ID) (*sqlite.Run, error) {
	runs, err := tx.RunsForTask(id)
	if err != nil {
		return nil, err
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Mode == verification.ModeGroup {
			r := runs[i]
			return &r, nil
		}
	}
	return nil, nil
}

// groupVerification derives the verification state of a group with
// checks for views and the summary.
func (e *Engine) groupVerification(tx *sqlite.Tx, rec sqlite.Record, proj project.Project, now time.Time) (GroupVerification, error) {
	var gv GroupVerification
	if !verification.HasRequired(rec.Task.Verification.TaskChecks) {
		return gv, nil
	}
	run, err := latestGroupRun(tx, rec.Task.ID)
	if err != nil {
		return gv, err
	}
	if run != nil {
		gv.RunID, gv.Revision, gv.Summary = run.ID, run.Revision, run.Summary
		gv.At = run.FinishedAt
		if run.Status == sqlite.RunRunning {
			if job, err := tx.RunningJob(proj.ID, groupJobName(rec.Task.ID), now); err != nil {
				return gv, err
			} else if job != nil {
				gv.Status = "running"
				return gv, nil
			}
		}
	}
	if rec.CompletedAt != nil {
		gv.Status = "passed"
		return gv, nil
	}
	ready, err := e.groupReady(tx, rec, proj, now)
	if err != nil || !ready {
		return gv, err
	}
	gv.Status = "pending"
	if run != nil && (run.Status == sqlite.RunFailed || run.Status == sqlite.RunError) {
		// Failed on the current target and contract: a planner must act.
		head, _ := e.manager(proj).Revision(context.Background(), "refs/heads/"+proj.TargetBranch)
		if run.Revision == head && run.Status == sqlite.RunFailed {
			gv.Status = "failed"
			return gv, nil
		}
	}
	if next, _, err := cohortBackoff(tx, proj.ID, groupJobName(rec.Task.ID)); err != nil {
		return gv, err
	} else if next.After(now) {
		gv.NextRetry = &next
	}
	return gv, nil
}

// groupJob is one claimed group verification.
type groupJob struct {
	id    int64
	owner execution.Token
	proj  project.Project
	rec   sqlite.Record
	runID int64
	head  string
}

// RunPendingGroups finds groups whose checks may run and runs them, one
// durable job each. Claim(wait) calls it beside RunPendingCohorts;
// `at verify groups` calls it on demand.
func (e *Engine) RunPendingGroups(ctx context.Context, pid project.ID, only task.ID) (bool, error) {
	if err := e.reconcile(ctx); err != nil {
		return false, err
	}
	ran := false
	for {
		job, ok, err := e.claimGroupJob(ctx, pid, only)
		if err != nil || !ok {
			return ran, err
		}
		ran = true
		if err := e.executeGroupJob(ctx, job); err != nil && fault.CodeOf(err) == fault.CodeInternal {
			return ran, err
		}
		e.notify()
		if only != "" {
			return ran, nil
		}
	}
}

func (e *Engine) claimGroupJob(ctx context.Context, pid project.ID, only task.ID) (groupJob, bool, error) {
	now := e.now()
	owner := execution.NewToken()
	var job groupJob
	found := false
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		proj, err := tx.GetProject(pid)
		if err != nil {
			return err
		}
		head, err := e.manager(proj).Revision(ctx, "refs/heads/"+proj.TargetBranch)
		if err != nil {
			return err
		}
		records, err := tx.ListRecords(pid, sqlite.ScopeLive, now, proj.MaxAttempts)
		if err != nil {
			return err
		}
		for _, r := range records {
			if r.Task.Kind != task.KindGroup || r.CompletedAt != nil || (only != "" && r.Task.ID != only) {
				continue
			}
			ready, err := e.groupReady(tx, r, proj, now)
			if err != nil || !ready {
				if err != nil {
					return err
				}
				continue
			}
			name := groupJobName(r.Task.ID)
			if running, err := tx.RunningJob(pid, name, now); err != nil {
				return err
			} else if running != nil {
				continue
			}
			last, err := latestGroupRun(tx, r.Task.ID)
			if err != nil {
				return err
			}
			// Nothing changed since a failure on this very revision and
			// contract: the planner has to act, not the verifier.
			if last != nil && last.Status == sqlite.RunFailed && last.Revision == head && last.PolicyDigest == r.Task.Verification.Digest() && only == "" {
				continue
			}
			if next, _, err := cohortBackoff(tx, pid, name); err != nil {
				return err
			} else if next.After(now) && only == "" {
				continue
			}
			jobID, err := tx.InsertJob(pid, name, nil, owner.Digest(), now.Add(execution.DefaultLease), now)
			if err != nil {
				return err
			}
			policy := verification.Policy{TaskChecks: r.Task.Verification.TaskChecks}
			runID, err := tx.InsertRun(sqlite.NewRun{TaskID: r.Task.ID, JobID: jobID, Mode: verification.ModeGroup, Status: sqlite.RunRunning, Revision: head, Policy: policy, Environment: environmentFingerprint()}, now)
			if err != nil {
				return err
			}
			job = groupJob{id: jobID, owner: owner, proj: proj, rec: r, runID: runID, head: head}
			found = true
			return nil
		}
		return nil
	})
	return job, found, err
}

// executeGroupJob runs the group's checks on a snapshot of the target
// head under the group's size budget and records the outcome: passed
// completes the group; failed records the evidence for the planner;
// an environment error backs off like a cohort job.
func (e *Engine) executeGroupJob(ctx context.Context, job groupJob) error {
	checkCtx, stop := e.jobHeartbeat(ctx, cohortJob{id: job.id, owner: job.owner, proj: job.proj, cohort: groupJobName(job.rec.Task.ID)})
	defer stop()
	fence := func(tx *sqlite.Tx) error {
		j, err := tx.GetJob(job.id)
		if err != nil {
			return err
		}
		if j.Status != "running" || j.OwnerDigest != job.owner.Digest() {
			return fault.New(fault.CodeSessionSuperseded, "group verification job %d lost ownership", job.id)
		}
		return nil
	}
	finish := func(run sqlite.RunStatus, jobStatus, summary string, complete bool) error {
		now := e.now()
		return e.store.Write(ctx, func(tx *sqlite.Tx) error {
			if err := fence(tx); err != nil {
				return err
			}
			if err := tx.FinishRun(job.runID, run, summary, now); err != nil {
				return err
			}
			if complete {
				if err := tx.MarkGroupComplete(job.rec.Task.ID, now); err != nil {
					return err
				}
			} else if err := tx.SetLastError(job.rec.Task.ID, 0, summary, now); err != nil {
				return err
			}
			return tx.FinishJob(job.id, job.owner.Digest(), jobStatus, job.head, summary, now)
		})
	}
	mgr := e.manager(job.proj)
	dir, cleanup, err := mgr.Snapshot(ctx, job.head)
	if err != nil {
		_ = finish(sqlite.RunError, "error", "cannot snapshot "+job.proj.TargetBranch+": "+fault.MessageOf(err), false)
		return err
	}
	defer cleanup()
	size := job.rec.Task.Size
	if size == "" {
		size = task.SizeLarge
	}
	policy := verification.Policy{TaskChecks: job.rec.Task.Verification.TaskChecks}
	opts := runOpts{budget: job.proj.Budgets.ForSize(size), budgetName: fmt.Sprintf("the %s budget of the group", size), target: job.proj.TargetBranch}
	evidence, err := e.runChecks(checkCtx, dir, policy, job.runID, job.head, fence, opts)
	if err != nil {
		_ = finish(sqlite.RunError, "error", "group verification interrupted: "+fault.MessageOf(err), false)
		return err
	}
	v := verification.Judge(policy, evidence)
	switch {
	case v.Passed:
		if err := finish(sqlite.RunPassed, "passed", v.Summary, true); err != nil {
			return err
		}
		return nil
	case len(v.TimedOut) > 0:
		summary := fmt.Sprintf("%s; group check(s) %s exceeded the %s budget (%s): the epic's verification is built too slow; narrow it or set a larger --size on the group", v.Summary, strings.Join(v.TimedOut, ", "), size, job.proj.Budgets.ForSize(size))
		_ = finish(sqlite.RunFailed, "failed", summary, false)
		return fault.New(fault.CodeVerificationTimeout, "group %s: %s", job.rec.Task.ID, summary)
	default:
		summary := v.Summary + "; the epic's checks failed on " + short(job.head) + ": add follow-up member tasks (they reopen the group) or fix the checks"
		_ = finish(sqlite.RunFailed, "failed", summary, false)
		return fault.New(fault.CodeVerificationFailed, "group %s: %s", job.rec.Task.ID, summary)
	}
}

// reopenAncestors withdraws the completion of every completed group that
// now has a new or reopened member: the epic must be verified again.
func reopenAncestors(tx *sqlite.Tx, parent task.ID, now time.Time) error {
	for parent != "" {
		rec, err := tx.GetRecord(parent)
		if err != nil {
			return err
		}
		if rec.CompletedAt != nil {
			if err := tx.ClearComplete(parent, now); err != nil {
				return err
			}
		}
		parent = rec.Task.ParentID
	}
	return nil
}

var _ = workspace.ErrTargetMoved
