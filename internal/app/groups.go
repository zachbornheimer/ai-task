package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/proof"

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

// targetHead is the target branch's head as resolved before a database
// transaction opens: Git never runs inside store.Read or store.Write.
// The zero value means it was not resolved (only task views, which never
// consult it, are built that way).
type targetHead struct {
	rev   string
	err   error
	known bool
}

// unresolvedReason says why the target branch could not be resolved.
func (h targetHead) unresolvedReason() (string, bool) {
	if h.err == nil {
		return "", false
	}
	return fault.MessageOf(h.err), true
}

// resolveTargetHead reads the project and resolves its target branch
// outside any transaction. A project that cannot be read yields the zero
// value; the caller's own read reports that error.
func (e *Engine) resolveTargetHead(ctx context.Context, pid project.ID) targetHead {
	var proj project.Project
	if err := e.store.Read(ctx, func(tx *sqlite.Tx) (err error) { proj, err = tx.GetProject(pid); return }); err != nil {
		return targetHead{}
	}
	return e.targetHeadOf(ctx, proj)
}

// projectHeads resolves the target branch of every enrolled git project.
// General and gitless projects are omitted; callers treat a missing
// entry as "no target" (epics there never need a head).
func (e *Engine) projectHeads(ctx context.Context) (map[project.ID]targetHead, error) {
	var ps []project.Project
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		var err error
		ps, err = tx.ListProjects()
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make(map[project.ID]targetHead, len(ps))
	for i := range ps {
		p := &ps[i]
		if p.IsGeneral() || p.RootPath == "" {
			continue
		}
		out[p.ID] = e.targetHeadOf(ctx, *p)
	}
	return out, nil
}

func (e *Engine) targetHeadOf(ctx context.Context, proj project.Project) targetHead {
	rev, err := e.manager(proj).Revision(ctx, "refs/heads/"+proj.TargetBranch)
	return targetHead{rev: rev, err: err, known: true}
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
	unverified, err := hasUnverifiedEpic(tx, rec.Task.ID)
	return !unverified, err
}

// hasUnverifiedEpic reports whether any group with checks beneath id, at
// any depth and through plain groups, is not yet complete.
func hasUnverifiedEpic(tx *sqlite.Tx, id task.ID) (bool, error) {
	children, err := tx.Children(id)
	if err != nil {
		return false, err
	}
	for i := range children {
		c := &children[i]
		if c.Task.Kind != task.KindGroup {
			continue
		}
		if verification.HasRequired(c.Task.Verification.TaskChecks) && c.CompletedAt == nil {
			return true, nil
		}
		if nested, err := hasUnverifiedEpic(tx, c.Task.ID); err != nil || nested {
			return nested, err
		}
	}
	return false, nil
}

// latestGroupRun is the newest group-mode run of a group, if any.
func latestGroupRun(tx *sqlite.Tx, id task.ID) (*sqlite.Run, error) {
	runs, err := tx.RunsForTask(id)
	if err != nil {
		return nil, err
	}
	for i := len(runs) - 1; i >= 0; i-- {
		r := &runs[i]
		if r.Mode == verification.ModeGroup {
			return r, nil
		}
	}
	return nil, nil
}

// groupVerification derives the verification state of a group with
// checks for views and the summary.
func (e *Engine) groupVerification(tx *sqlite.Tx, rec sqlite.Record, proj project.Project, now time.Time, head targetHead) (GroupVerification, error) {
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
	if !head.known {
		return gv, fault.NewInternal("group %s: the target head was not resolved for this view", rec.Task.ID)
	}
	if reason, unresolved := head.unresolvedReason(); unresolved {
		// Without the target the epic can neither run nor be judged
		// current: say so rather than reading as waiting for a verifier.
		gv.Status = "error"
		gv.Summary = fmt.Sprintf("cannot resolve target branch %s: %s", proj.TargetBranch, reason)
		return gv, nil
	}
	gv.Status = "pending"
	if run != nil && (run.Status == sqlite.RunFailed || run.Status == sqlite.RunError) {
		// Failed on the current target and contract: a planner must act.
		reqs, err := groupRequirements(tx, rec.Task.ID)
		if err != nil {
			return gv, err
		}
		if run.Revision == groupInputRevision(head.rev, reqs) && run.Status == sqlite.RunFailed {
			gv.Status = "failed"
			return gv, nil
		}
	}
	if next, err := jobBackoff(tx, proj.ID, groupJobName(rec.Task.ID)); err != nil {
		return gv, err
	} else if next.After(now) {
		gv.NextRetry = &next
	}
	return gv, nil
}

// groupInputRevision names what a group's proofs are judged on: the target
// head and the completed revision of every task beneath the group. A failed
// run is not repeated until one of them changes.
func groupInputRevision(head string, reqs []workspace.Requirement) string {
	lines := make([]string, 0, len(reqs))
	for _, r := range reqs {
		lines = append(lines, r.Task+"="+r.Revision)
	}
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return head + "+" + hex.EncodeToString(sum[:6])
}

// groupJob is one claimed group verification.
type groupJob struct {
	id           int64
	owner        execution.Token
	proj         project.Project
	rec          sqlite.Record
	runID        int64
	head         string
	requirements []workspace.Requirement
}

// groupRequirements lists the completed revision of every executable task
// beneath a group, at any depth: what the group's proofs are judged on.
func groupRequirements(tx *sqlite.Tx, id task.ID) ([]workspace.Requirement, error) {
	children, err := tx.Children(id)
	if err != nil {
		return nil, err
	}
	var out []workspace.Requirement
	for i := range children {
		c := &children[i]
		switch {
		case c.Task.Archived():
		case c.Task.Kind == task.KindGroup:
			nested, err := groupRequirements(tx, c.Task.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, nested...)
		case c.CompletedRevision != "":
			out = append(out, workspace.Requirement{Task: string(c.Task.ID), Revision: c.CompletedRevision})
		}
	}
	return out, nil
}

// RunPendingGroups finds groups whose proofs may run and runs them, one
// durable job each. Claim(wait) calls it so any worker with capacity
// verifies a ready group.
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
		if _, err := e.executeGroupJob(ctx, job); err != nil && fault.IsInternal(err) {
			return ran, err
		}
		e.notify()
		if only != "" {
			return ran, nil
		}
	}
}

// VerifyGroup verifies one group now: its proofs run on the merge of every
// child's completed revision and, when they pass, the group completes. It
// fails with NOT_READY until every child is complete, and with
// BASE_CONFLICT when the children's revisions cannot be merged.
func (e *Engine) VerifyGroup(ctx context.Context, pid project.ID, ref string) (VerifyResult, error) {
	v, err := e.Show(ctx, pid, ref, false)
	if err != nil {
		return VerifyResult{}, err
	}
	if v.Kind != task.KindGroup {
		return VerifyResult{}, fault.New(fault.CodeInvalidInput, "%s is not a group; tasks complete with `verify complete` in their claim", ref)
	}
	if v.CompletedAt != nil {
		return VerifyResult{TaskID: v.ID, Mode: verification.ModeGroup, Passed: true, Completed: true, Status: task.StatusComplete, Message: "group already complete"}, nil
	}
	if err := e.reconcile(ctx); err != nil {
		return VerifyResult{}, err
	}
	if err := e.requireGroupReady(ctx, pid, v.ID); err != nil {
		return VerifyResult{}, err
	}
	job, ok, err := e.claimGroupJob(ctx, pid, v.ID)
	if err != nil {
		return VerifyResult{}, err
	}
	if !ok {
		return VerifyResult{}, fault.New(fault.CodeConflict, "%s: another verifier is running this group; wait for its result", v.ID)
	}
	res, err := e.executeGroupJob(ctx, job)
	e.notify()
	writeRunResult(res, err)
	return res, err
}

// requireGroupReady fails with NOT_READY, saying how many children are
// still open, unless every child of the group is complete.
func (e *Engine) requireGroupReady(ctx context.Context, pid project.ID, id task.ID) error {
	now := e.now()
	return e.store.Read(ctx, func(tx *sqlite.Tx) error {
		proj, err := tx.GetProject(pid)
		if err != nil {
			return err
		}
		rec, err := tx.GetRecord(id)
		if err != nil {
			return err
		}
		if !verification.HasRequired(rec.Task.Verification.TaskChecks) {
			return fault.New(fault.CodeInvalidInput, "%s has no required proof to verify (add one with --proof)", id)
		}
		ready, err := e.groupReady(tx, rec, proj, now)
		if err != nil || ready {
			return err
		}
		prog, err := e.groupProgress(tx, id, proj, now)
		if err != nil {
			return err
		}
		return fault.New(fault.CodeNotReady, "%s is not ready: %d of %d children are complete; a group verifies only after every child completes", id, prog.Complete, prog.Total)
	})
}

func (e *Engine) claimGroupJob(ctx context.Context, pid project.ID, only task.ID) (groupJob, bool, error) {
	now := e.now()
	owner := execution.NewToken()
	var job groupJob
	found := false
	// Resolve the target before the transaction: Git never runs inside
	// the database lock. A failure is the caller's error, not "pending".
	head := e.resolveTargetHead(ctx, pid)
	if head.known && head.err != nil {
		return job, false, head.err
	}
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		proj, err := tx.GetProject(pid)
		if err != nil {
			return err
		}
		records, err := tx.ListRecords(pid, sqlite.ScopeLive, now, proj.MaxAttempts)
		if err != nil {
			return err
		}
		for i := range records {
			r := &records[i]
			if r.Task.Kind != task.KindGroup || r.CompletedAt != nil || (only != "" && r.Task.ID != only) {
				continue
			}
			ready, err := e.groupReady(tx, *r, proj, now)
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
			requirements, err := groupRequirements(tx, r.Task.ID)
			if err != nil {
				return err
			}
			input := groupInputRevision(head.rev, requirements)
			// Nothing changed since a failure on this very revision and
			// contract: the planner has to act, not the verifier.
			if last != nil && last.Status == sqlite.RunFailed && last.Revision == input && last.PolicyDigest == effectivePolicy(r.Task, proj).Digest() && only == "" {
				continue
			}
			if next, err := jobBackoff(tx, pid, name); err != nil {
				return err
			} else if next.After(now) && only == "" {
				continue
			}
			jobID, err := tx.InsertJob(pid, name, nil, owner.Digest(), now.Add(execution.DefaultLease), now)
			if err != nil {
				return err
			}
			policy := effectivePolicy(r.Task, proj)
			runID, err := tx.InsertRun(sqlite.NewRun{TaskID: r.Task.ID, JobID: jobID, Mode: verification.ModeGroup, Status: sqlite.RunRunning, Revision: input, Policy: policy, Environment: environmentFingerprint()}, now)
			if err != nil {
				return err
			}
			job = groupJob{id: jobID, owner: owner, proj: proj, rec: *r, runID: runID, head: head.rev, requirements: requirements}
			found = true
			return nil
		}
		return nil
	})
	return job, found, err
}

// executeGroupJob runs the group's proof and regression on the merge of
// every child's completed revision, built in a scratch worktree (no
// integration branch exists), under the group's size budget. Passed
// completes the group; failed records the evidence for the planner; an
// environment error backs off before the next attempt.
func (e *Engine) executeGroupJob(ctx context.Context, job groupJob) (VerifyResult, error) {
	res := VerifyResult{TaskID: job.rec.Task.ID, Mode: verification.ModeGroup, RunID: job.runID, Revision: job.head, LogDir: e.runLogDir(job.proj.ID, job.runID)}
	checkCtx, stop := e.jobHeartbeat(ctx, jobLease{id: job.id, owner: job.owner})
	defer stop()
	fence := func(tx *sqlite.Tx) error {
		j, err := tx.GetJob(job.id)
		if err != nil {
			return err
		}
		if j.Status != "running" || j.OwnerDigest != job.owner.Digest() {
			return fault.New(fault.CodeInvalidSession, "group verification job %d lost ownership", job.id)
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
			return tx.FinishJob(job.id, job.owner.Digest(), jobStatus, res.Revision, summary, now)
		})
	}
	fail := func(code fault.Code, summary string) (VerifyResult, error) {
		res.Summary, res.Status = summary, e.statusOf(ctx, job.rec.Task.ID)
		return res, &fault.Error{Code: code, Message: fmt.Sprintf("group %s: %s", job.rec.Task.ID, summary), Details: res}
	}
	mgr := e.manager(job.proj)
	resolved, err := mgr.ResolveGroupBase(ctx, job.proj.TargetBranch, job.requirements)
	if err != nil {
		summary := "cannot merge the children's completed revisions: " + fault.MessageOf(err)
		status, jobStatus := sqlite.RunError, "error"
		if fault.CodeOf(err) == fault.CodeBaseConflict {
			status, jobStatus = sqlite.RunFailed, "failed"
		}
		_ = finish(status, jobStatus, summary, false)
		return fail(fault.CodeOf(err), summary)
	}
	base := resolved.Revision
	res.Revision, res.Base = base, resolved.Explain()
	size := job.rec.Task.Size
	if size == "" {
		size = task.SizeLarge
	}
	t := job.rec.Task
	t.Size = size
	policy := effectivePolicy(t, job.proj)
	staleEvidence, staleFailure, err := e.proveStaleChildren(checkCtx, mgr, base, resolved.Stale, job, fence)
	res.Evidence = staleEvidence
	if err == nil && staleFailure != "" {
		_ = finish(sqlite.RunFailed, "failed", staleFailure, false)
		return fail(fault.CodeCheckFailed, staleFailure)
	}
	evidence, err2 := e.runSuites(checkCtx, mgr, base, policy, job.runID, fence, t, job.proj, "")
	if err == nil {
		err = err2
	}
	evidence = append(staleEvidence, evidence...)
	res.Evidence = evidence
	if err != nil {
		_ = finish(sqlite.RunError, "error", "group verification interrupted: "+fault.MessageOf(err), false)
		return res, err
	}
	v := verification.Judge(policy, evidence)
	switch {
	case v.Passed:
		if err := finish(sqlite.RunPassed, "passed", v.Summary, true); err != nil {
			return res, err
		}
		res.Passed, res.Completed, res.Status = true, true, task.StatusComplete
		res.Summary, res.Message = v.Summary, "group complete: its proofs passed on the merge of every child's completed revision"
		return res, nil
	case len(v.TimedOut) > 0:
		summary := fmt.Sprintf("%s; group check(s) %s exceeded the %s budget (%s): the group's verification is built too slow; narrow it or set a larger --size on the group", v.Summary, strings.Join(v.TimedOut, ", "), size, e.sizeBudget(proof.Size(size)))
		_ = finish(sqlite.RunFailed, "failed", summary, false)
		return fail(fault.CodeCheckFailed, summary)
	default:
		summary := v.Summary + "; the group's proofs failed on the merge of its children (" + short(base) + "): add follow-up child tasks (they reopen the group) or fix the proofs"
		_ = finish(sqlite.RunFailed, "failed", summary, false)
		return fail(fault.CodeCheckFailed, summary)
	}
}

// proveStaleChildren runs the task proofs of children whose completed
// revision the target has since superseded, on the base (the target tip
// plus the clean children). A child counts as landed when its proof
// passes there. The failure string names the first child whose proof did not.
func (e *Engine) proveStaleChildren(ctx context.Context, mgr workspace.Manager, base string, stale []workspace.Requirement, job groupJob, fence func(*sqlite.Tx) error) ([]verification.Evidence, string, error) {
	var out []verification.Evidence
	for _, req := range stale {
		var child sqlite.Record
		if err := e.store.Read(ctx, func(tx *sqlite.Tx) (err error) {
			child, err = tx.GetRecord(task.ID(req.Task))
			return err
		}); err != nil {
			return out, "", err
		}
		policy := verification.Policy{TaskChecks: effectivePolicy(child.Task, job.proj).TaskChecks}
		ev, err := e.runSuites(ctx, mgr, base, policy, job.runID, fence, child.Task, job.proj, "")
		out = append(out, ev...)
		if err != nil {
			return out, "", err
		}
		if v := verification.Judge(policy, ev); !v.Passed {
			return out, fmt.Sprintf("%s %s: its proof failed on the target (%s) after the target changed its completed revision: %s", child.Task.ID, child.Task.Description, short(base), v.Summary), nil
		}
	}
	return out, "", nil
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
