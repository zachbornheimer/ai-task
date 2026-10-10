package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

// submitForCohort records an immutable submission for a cohort member and
// ends its claim; the consumed token never becomes a cohort credential.
// If every peer has submitted, the cohort job runs now under its own
// fenced authority; otherwise the task waits as awaiting_verification.
func (e *Engine) submitForCohort(ctx context.Context, fr finalRun) (VerifyResult, error) {
	now := e.now()
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		if _, err := e.touch(tx, fr.token, now); err != nil {
			return err
		}
		r, err := tx.GetRecord(fr.rec.Task.ID)
		if err != nil {
			return err
		}
		if r.UnmetRequires > 0 {
			return fault.New(fault.CodeTaskBlocked, "%s cannot submit: prerequisites changed", r.Task.ID)
		}
		if fr.submission, err = tx.InsertSubmission(r.Task.ID, fr.attempt.ID, fr.revision, r.Task.Cohort, now); err != nil {
			return err
		}
		return tx.EndAttempt(fr.attempt.ID, execution.EndSubmitted, now)
	})
	if err != nil {
		return VerifyResult{}, err
	}
	e.notify()
	res := VerifyResult{TaskID: fr.rec.Task.ID, Mode: verification.ModeComplete, Revision: fr.revision, SubmissionID: fr.submission, Submitted: true}
	ran, err := e.RunPendingCohorts(ctx, fr.proj.ID)
	if err != nil && fault.CodeOf(err) == fault.CodeInternal {
		return res, err
	}
	res.Status = e.statusOf(ctx, fr.rec.Task.ID)
	res.Completed = res.Status == task.StatusComplete
	res.Passed = res.Completed
	switch {
	case res.Completed:
		res.Message = "cohort verified; task complete"
		return res, nil
	case !ran:
		res.Message = fmt.Sprintf("submitted for cohort %q; waiting for peers to submit (claim ended)", fr.rec.Task.Cohort)
		return res, nil
	case res.Status == task.StatusAwaitingVerification:
		res.Message = fmt.Sprintf("submitted; cohort %q verification could not finish yet and will be retried by the next verifier (see `at show`)", fr.rec.Task.Cohort)
		return res, nil
	}
	res.Message = "cohort verification failed for this task; it is claimable for repair"
	return res, &fault.Error{Code: fault.CodeVerificationFailed, Message: fmt.Sprintf("cohort %q verification did not pass for %s", fr.rec.Task.Cohort, fr.rec.Task.ID), Details: res}
}

// cohortJob is the in-memory state of one running job.
type cohortJob struct {
	id          int64
	owner       execution.Token
	proj        project.Project
	cohort      string
	members     []cohortMember
	regressionD string
}

type cohortMember struct {
	rec         sqlite.Record
	submission  int64
	revision    string
	runID       int64
	policy      verification.Policy
	contractRev int
}

// RunPendingCohorts finds cohorts whose members have all submitted and no
// verifier is running, claims each as a durable job, and verifies it. It
// returns whether any job ran. Claim(wait) calls it so that pending cohort
// work is executed by whichever worker has capacity, and a restart
// recovers jobs whose verifier died (their lease expires).
func (e *Engine) RunPendingCohorts(ctx context.Context, pid project.ID) (bool, error) {
	if err := e.reconcile(ctx); err != nil {
		return false, err
	}
	ran := false
	for {
		job, ok, err := e.claimCohortJob(ctx, pid)
		if err != nil || !ok {
			return ran, err
		}
		ran = true
		if err := e.executeCohortJob(ctx, job); err != nil && fault.CodeOf(err) == fault.CodeInternal {
			return ran, err
		}
		e.notify()
	}
}

// claimCohortJob picks one runnable cohort and records a running job
// owned by a fresh verifier token, with one running run per member.
func (e *Engine) claimCohortJob(ctx context.Context, pid project.ID) (cohortJob, bool, error) {
	now := e.now()
	owner := execution.NewToken()
	var job cohortJob
	found := false
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		proj, err := tx.GetProject(pid)
		if err != nil {
			return err
		}
		records, err := tx.ListRecords(pid, sqlite.ScopeOpen, now, proj.MaxAttempts)
		if err != nil {
			return err
		}
		byCohort := map[string][]sqlite.Record{}
		var order []string
		for _, r := range records {
			if r.Task.Kind == task.KindTask && r.Task.Cohort != "" {
				if _, seen := byCohort[r.Task.Cohort]; !seen {
					order = append(order, r.Task.Cohort)
				}
				byCohort[r.Task.Cohort] = append(byCohort[r.Task.Cohort], r)
			}
		}
		for _, cohort := range order {
			members := byCohort[cohort]
			runnable := true
			for _, m := range members {
				if m.Submission == nil || m.Submission.RunStatus() != sqlite.RunPending {
					runnable = false
					break
				}
			}
			if !runnable {
				continue
			}
			if running, err := tx.RunningJob(pid, cohort, now); err != nil {
				return err
			} else if running != nil {
				continue
			}
			// Environment failures (a promotion that cannot land) back off
			// instead of re-running the whole verification in a loop.
			if next, _, err := cohortBackoff(tx, pid, cohort); err != nil {
				return err
			} else if next.After(now) {
				continue
			}
			var jm []sqlite.JobMember
			for _, m := range members {
				jm = append(jm, sqlite.JobMember{TaskID: m.Task.ID, SubmissionID: m.Submission.ID, Revision: m.Submission.Revision})
			}
			jobID, err := tx.InsertJob(pid, cohort, jm, owner.Digest(), now.Add(execution.DefaultLease), now)
			if err != nil {
				return err
			}
			job = cohortJob{id: jobID, owner: owner, proj: proj, cohort: cohort, regressionD: (verification.Policy{Regression: proj.Regression}).Digest()}
			for _, m := range members {
				policy := effectivePolicy(m.Task, proj)
				runID, err := tx.InsertRun(sqlite.NewRun{TaskID: m.Task.ID, AttemptID: m.Submission.AttemptID, SubmissionID: m.Submission.ID, JobID: jobID, Mode: verification.ModeCohort, Status: sqlite.RunRunning, Revision: m.Submission.Revision, Policy: policy, Environment: environmentFingerprint()}, now)
				if err != nil {
					return err
				}
				job.members = append(job.members, cohortMember{rec: m, submission: m.Submission.ID, revision: m.Submission.Revision, runID: runID, policy: policy, contractRev: m.Task.ContractRev})
			}
			found = true
			return nil
		}
		return nil
	})
	return job, found, err
}

// Cohort retry backoff after a job that ended in an environment error:
// 30s, 1m, 2m, ... capped at 30m, counted over consecutive error jobs.
const (
	cohortBackoffBase = 30 * time.Second
	cohortBackoffCap  = 30 * time.Minute
)

// cohortBackoff returns when the cohort may be retried and how many
// consecutive environment errors precede it (zero when none).
func cohortBackoff(tx *sqlite.Tx, pid project.ID, cohort string) (time.Time, int, error) {
	jobs, err := tx.RecentJobs(pid, cohort, 16)
	if err != nil {
		return time.Time{}, 0, err
	}
	n := 0
	for _, j := range jobs {
		if j.Status != "error" || j.FinishedAt == nil {
			break
		}
		n++
	}
	if n == 0 {
		return time.Time{}, 0, nil
	}
	d := cohortBackoffBase << uint(n-1)
	if n > 12 || d > cohortBackoffCap {
		d = cohortBackoffCap
	}
	return jobs[0].FinishedAt.Add(d), n, nil
}

// cohortMemberConflict says which member's re-check refused finalisation.
type cohortMemberConflict struct {
	member task.ID
	err    error
}

func (c cohortMemberConflict) Error() string { return c.err.Error() }
func (c cohortMemberConflict) Unwrap() error { return c.err }

// executeCohortJob assembles one candidate from every member revision,
// runs each member's task checks and the regression checks fresh on it,
// promotes when required, and finalises eligible members atomically.
func (e *Engine) executeCohortJob(ctx context.Context, job cohortJob) error {
	checkCtx, stop := e.jobHeartbeat(ctx, job)
	defer stop()
	fence := func(tx *sqlite.Tx) error {
		j, err := tx.GetJob(job.id)
		if err != nil {
			return err
		}
		if j.Status != "running" || j.OwnerDigest != job.owner.Digest() {
			return fault.New(fault.CodeSessionSuperseded, "verification job %d lost ownership", job.id)
		}
		return nil
	}
	// finishAll ends every member run. A genuine proof failure counts
	// against each member's submitting attempt (once); anything else is
	// recorded as the members' last_error.
	finishAll := func(status sqlite.RunStatus, summary string, failed bool) error {
		now := e.now()
		err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
			if err := fence(tx); err != nil {
				return err
			}
			for _, m := range job.members {
				if err := tx.FinishRun(m.runID, status, summary, now); err != nil {
					return err
				}
				if failed {
					if _, err := tx.RecordFailure(m.rec.Task.ID, m.rec.Submission.AttemptID, job.proj.RetryCooldown, now); err != nil {
						return err
					}
				} else if err := tx.SetLastError(m.rec.Task.ID, 0, summary, now); err != nil {
					return err
				}
			}
			return tx.FinishJob(job.id, job.owner.Digest(), "failed", "", summary, now)
		})
		return err
	}
	// Assemble the candidate, verify every member on it, and promote. If the
	// target moves between assembly and promotion, rebuild on the new base
	// (bounded) so stale content is never promoted.
	mgr := e.manager(job.proj)
	var cand workspace.Candidate
	candidate := ""
	evidence := map[int64][]verification.Evidence{}
	for attempt := 0; attempt < 3; attempt++ {
		evidence = map[int64][]verification.Evidence{}
		var sources []string
		for _, m := range job.members {
			sources = append(sources, m.revision)
		}
		var err error
		cand, err = mgr.PrepareMerge(ctx, job.proj.TargetBranch, sources, fmt.Sprintf("at: integrate cohort %s", job.cohort))
		if err != nil {
			// A conflicting merge is for the members to resolve, not a strike.
			_ = finishAll(sqlite.RunFailed, "cohort candidate could not be assembled: "+err.Error(), false)
			return err
		}
		candidate = cand.Revision
		outcome, err := e.judgeCohort(checkCtx, job, mgr, candidate, evidence, fence)
		if err != nil {
			cand.Cleanup()
			return err
		}
		if !outcome {
			cand.Cleanup()
			return fault.New(fault.CodeVerificationFailed, "cohort %q verification failed", job.cohort)
		}
		if job.proj.Integration != project.IntegrationPromote {
			cand.Cleanup()
			return e.finalizeCohortDirect(ctx, job, candidate, fence, finishAll)
		}
		// Durable, recoverable promotion (see integrate.go): pin one intent
		// per member, compare-and-swap the target, then complete all members.
		intents, err := e.recordCohortIntents(ctx, job, cand, candidate, fence)
		if err != nil {
			cand.Cleanup()
			if fault.CodeOf(err) != fault.CodeInternal {
				_ = e.finishConflict(ctx, job, fence, err)
			}
			return err
		}
		if err := e.fault("cohort:after-intent"); err != nil {
			cand.Cleanup()
			return err
		}
		err = e.fault("cohort:promote")
		if err == nil {
			err = mgr.Promote(ctx, job.proj.TargetBranch, cand)
		}
		cand.Cleanup()
		if err != nil {
			for _, id := range intents {
				e.abandonIntent(ctx, id, "promotion did not happen: "+err.Error())
			}
			if errors.Is(err, workspace.ErrTargetMoved) && attempt < 2 {
				continue
			}
			// Candidate is fine; promotion is not. Put members back to pending
			// so the next verifier rebuilds the candidate.
			now := e.now()
			_ = e.store.Write(ctx, func(tx *sqlite.Tx) error {
				if err := fence(tx); err != nil {
					return err
				}
				for _, m := range job.members {
					if err := tx.FinishRun(m.runID, sqlite.RunPending, "candidate passed but promotion failed: "+err.Error(), now); err != nil {
						return err
					}
					if err := tx.SetLastError(m.rec.Task.ID, 0, "INTEGRATION_FAILED: "+err.Error(), now); err != nil {
						return err
					}
				}
				return tx.FinishJob(job.id, job.owner.Digest(), "error", candidate, err.Error(), now)
			})
			return err
		}
		if err := e.fault("cohort:after-promote"); err != nil {
			return err
		}
		now := e.now()
		err = e.store.Write(ctx, func(tx *sqlite.Tx) error {
			for _, id := range intents {
				in, err := tx.GetIntent(id)
				if err != nil {
					return err
				}
				if err := e.completeIntentTx(tx, in, job.owner.Digest(), "cohort verified on candidate "+short(candidate)); err != nil {
					return err
				}
			}
			return tx.FinishJob(job.id, job.owner.Digest(), "passed", candidate, "cohort verified", now)
		})
		if err != nil {
			// The intents stay open; reconciliation completes them from Git.
			return fault.Wrap(err, fault.CodeInternal, "record completion of promoted cohort %q (will be reconciled)", job.cohort)
		}
		if err := e.fault("cohort:after-complete"); err != nil {
			return err
		}
		return nil
	}
	return nil
}

// cohortSpec builds the intent spec of one member.
func cohortSpec(job cohortJob, m cohortMember) intentSpec {
	return intentSpec{proj: job.proj, taskID: m.rec.Task.ID, jobID: job.id, runID: m.runID, submission: m.submission, owner: job.owner.Digest(), contractRev: m.contractRev, regression: job.regressionD, policy: m.policy, source: m.revision}
}

// recordCohortIntents re-checks every member under the job fence and pins
// one intent per member for the same candidate.
func (e *Engine) recordCohortIntents(ctx context.Context, job cohortJob, cand workspace.Candidate, candidate string, fence func(*sqlite.Tx) error) ([]int64, error) {
	now := e.now()
	var ids []int64
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		if err := fence(tx); err != nil {
			return err
		}
		for _, m := range job.members {
			s := cohortSpec(job, m)
			if _, err := e.recheckForCompletion(tx, s, candidate); err != nil {
				return cohortMemberConflict{member: m.rec.Task.ID, err: err}
			}
			if err := tx.FinishRun(m.runID, sqlite.RunPassed, "cohort verified on candidate "+short(candidate), now); err != nil {
				return err
			}
			if candidate != m.revision {
				if err := tx.SetRunIntegratedRevision(m.runID, candidate); err != nil {
					return err
				}
			}
			id, err := tx.InsertIntent(sqlite.Intent{ProjectID: job.proj.ID, TaskID: m.rec.Task.ID, JobID: job.id, RunID: m.runID, SubmissionID: m.submission, OwnerDigest: s.owner, ContractRev: m.contractRev, RegressionDigest: job.regressionD, TargetBranch: job.proj.TargetBranch, BaseRevision: cand.Base, SourceRevision: m.revision, CandidateRevision: candidate}, now)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	})
	if err == nil {
		e.notify()
	}
	return ids, err
}

// finishConflict ends a job whose finalisation was refused. When one
// member's re-check failed (late prerequisite, contract change, archive,
// resubmission), only that member goes back; its peers keep their
// submissions and wait for it. Nothing is counted against anyone.
func (e *Engine) finishConflict(ctx context.Context, job cohortJob, fence func(*sqlite.Tx) error, cause error) error {
	now := e.now()
	var mc cohortMemberConflict
	culprit := task.ID("")
	if errors.As(cause, &mc) {
		culprit = mc.member
	}
	return e.store.Write(ctx, func(tx *sqlite.Tx) error {
		if err := fence(tx); err != nil {
			return err
		}
		for _, m := range job.members {
			id := m.rec.Task.ID
			switch {
			case culprit == "" || id == culprit:
				if err := tx.FinishRun(m.runID, sqlite.RunFailed, "cohort finalisation refused: "+fault.MessageOf(cause), now); err != nil {
					return err
				}
				if err := tx.SetLastError(id, 0, "PLAN_CONFLICT: "+fault.MessageOf(cause), now); err != nil {
					return err
				}
			default:
				if err := tx.FinishRun(m.runID, sqlite.RunPending, "waiting for cohort peer "+string(culprit)+" to be repaired", now); err != nil {
					return err
				}
			}
		}
		return tx.FinishJob(job.id, job.owner.Digest(), "failed", "", "cohort finalisation refused: "+fault.MessageOf(cause), now)
	})
}

// finalizeCohortDirect completes a cohort that needs no promotion in one
// fenced transaction with the same re-checks.
func (e *Engine) finalizeCohortDirect(ctx context.Context, job cohortJob, candidate string, fence func(*sqlite.Tx) error, finishAll func(sqlite.RunStatus, string, bool) error) error {
	now := e.now()
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		if err := fence(tx); err != nil {
			return err
		}
		for _, m := range job.members {
			if _, err := e.recheckForCompletion(tx, cohortSpec(job, m), candidate); err != nil {
				return cohortMemberConflict{member: m.rec.Task.ID, err: err}
			}
		}
		for _, m := range job.members {
			if err := tx.FinishRun(m.runID, sqlite.RunPassed, "cohort verified on candidate "+short(candidate), now); err != nil {
				return err
			}
			if candidate != m.revision {
				_ = tx.SetRunIntegratedRevision(m.runID, candidate)
			}
			if err := tx.MarkComplete(m.rec.Task.ID, m.submission, now); err != nil {
				return err
			}
		}
		return tx.FinishJob(job.id, job.owner.Digest(), "passed", candidate, "cohort verified", now)
	})
	if err != nil {
		if fault.CodeOf(err) != fault.CodeInternal {
			_ = e.finishConflict(ctx, job, fence, err)
		}
		return err
	}
	return nil
}

// jobHeartbeat renews the verifier's job lease while checks run. Like
// heartbeat, it returns the context check processes must use: lost
// ownership cancels it at once; a transient error is retried sooner.
func (e *Engine) jobHeartbeat(ctx context.Context, job cohortJob) (context.Context, func()) {
	opCtx, cancelOp := context.WithCancel(ctx)
	hctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		interval := e.heartbeatEvery
		t := time.NewTimer(interval)
		defer t.Stop()
		for {
			select {
			case <-hctx.Done():
				return
			case <-t.C:
			}
			err := e.store.Write(hctx, func(tx *sqlite.Tx) error {
				return tx.RenewJob(job.id, job.owner.Digest(), e.now().Add(execution.DefaultLease))
			})
			switch {
			case err == nil:
				t.Reset(interval)
			case errors.Is(err, context.Canceled):
				return
			case terminalAuthority(err):
				cancelOp()
				return
			default:
				t.Reset(interval / 5)
			}
		}
	}()
	return opCtx, func() { cancel(); <-done; cancelOp() }
}

// judgeCohort runs every member's task checks and the shared regression
// suite on dir (the candidate) and records per-member verdicts. It returns
// true when every member passed; otherwise it records failures (sending
// failing members back for repair) and returns false.
func (e *Engine) judgeCohort(ctx context.Context, job cohortJob, mgr workspace.Manager, candidate string, evidence map[int64][]verification.Evidence, fence func(*sqlite.Tx) error) (bool, error) {
	// Every suite runs on its own disposable snapshot of the candidate so
	// no member's checks can leave anything behind for another's, or for
	// the shared regression suite.
	changed, _ := workspace.ChangedBetween(ctx, mgr.Repo, "refs/heads/"+job.proj.TargetBranch, candidate)
	runOn := func(policy verification.Policy, runID int64, opts runOpts) ([]verification.Evidence, error) {
		dir, cleanup, err := mgr.Snapshot(ctx, candidate)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		return e.runChecks(ctx, dir, policy, runID, candidate, fence, opts)
	}
	finishAll := func(status sqlite.RunStatus, summary string) error {
		now := e.now()
		return e.store.Write(ctx, func(tx *sqlite.Tx) error {
			if err := fence(tx); err != nil {
				return err
			}
			for _, m := range job.members {
				if err := tx.FinishRun(m.runID, status, summary, now); err != nil {
					return err
				}
			}
			return tx.FinishJob(job.id, job.owner.Digest(), "error", candidate, summary, now)
		})
	}
	verdicts := map[int64]verification.Verdict{}
	anyTaskFailed := false
	for _, m := range job.members {
		taskOpts, _ := suiteOpts(m.rec.Task, job.proj, changed)
		ev, err := runOn(verification.Policy{TaskChecks: m.policy.TaskChecks}, m.runID, taskOpts)
		evidence[m.runID] = ev
		if err != nil {
			_ = finishAll(sqlite.RunError, err.Error())
			return false, err
		}
		if v := verification.Judge(verification.Policy{TaskChecks: m.policy.TaskChecks}, ev); !v.Passed {
			verdicts[m.runID] = v
			anyTaskFailed = true
		}
	}
	regressionPassed := true
	var regressionSummary string
	if !anyTaskFailed {
		// Record the shared regression evidence under every member run so
		// each run's evidence alone proves its policy.
		first := job.members[0]
		_, regOpts := suiteOpts(first.rec.Task, job.proj, changed)
		ev, err := runOn(verification.Policy{Regression: job.proj.Regression}, first.runID, regOpts)
		if err != nil {
			_ = finishAll(sqlite.RunError, err.Error())
			return false, err
		}
		evidence[first.runID] = append(evidence[first.runID], ev...)
		for _, m := range job.members[1:] {
			for _, row := range ev {
				row.RunID, row.ID, row.Reused, row.ReusedFrom = m.runID, 0, false, 0
				row.Message = "shared cohort regression run"
				_ = e.store.Write(ctx, func(tx *sqlite.Tx) error { _, err := tx.InsertEvidence(row); return err })
				evidence[m.runID] = append(evidence[m.runID], row)
			}
		}
		v := verification.Judge(verification.Policy{Regression: job.proj.Regression}, ev)
		regressionPassed, regressionSummary = v.Passed, v.Summary
	}
	if !anyTaskFailed && regressionPassed {
		return true, nil
	}
	now := e.now()
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		if err := fence(tx); err != nil {
			return err
		}
		for _, m := range job.members {
			switch {
			case !regressionPassed:
				// The shared suite failed on the combined candidate: which
				// member broke it is unknown, so every member goes back for
				// repair with the evidence, and nobody is charged a failure.
				if err := tx.FinishRun(m.runID, sqlite.RunFailed, "cohort regression failed on candidate "+short(candidate)+": "+regressionSummary, now); err != nil {
					return err
				}
				if err := tx.SetLastError(m.rec.Task.ID, 0, "cohort regression failed on the combined candidate (no single member is to blame): "+regressionSummary, now); err != nil {
					return err
				}
			case verdicts[m.runID].Summary != "":
				if err := tx.FinishRun(m.runID, sqlite.RunFailed, "task checks failed on cohort candidate "+short(candidate)+": "+verdicts[m.runID].Summary, now); err != nil {
					return err
				}
				if _, err := tx.RecordFailure(m.rec.Task.ID, m.rec.Submission.AttemptID, job.proj.RetryCooldown, now); err != nil {
					return err
				}
			default:
				// This member passed; it waits for its peers' repair.
				if err := tx.FinishRun(m.runID, sqlite.RunPending, "task checks passed; waiting for cohort peers to be repaired", now); err != nil {
					return err
				}
			}
		}
		return tx.FinishJob(job.id, job.owner.Digest(), "failed", candidate, "cohort verification failed", now)
	})
	return false, err
}
