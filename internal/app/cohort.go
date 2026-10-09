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

// executeCohortJob assembles one candidate from every member revision,
// runs each member's task checks and the regression checks fresh on it,
// promotes when required, and finalises eligible members atomically.
func (e *Engine) executeCohortJob(ctx context.Context, job cohortJob) error {
	stop := e.jobHeartbeat(ctx, job)
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
					if err := tx.RecordFailure(m.rec.Task.ID, job.proj.RetryCooldown, now); err != nil {
						return err
					}
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
			_ = finishAll(sqlite.RunFailed, "cohort candidate could not be assembled: "+err.Error(), true)
			return err
		}
		candidate = cand.Revision
		dir, cleanup, err := mgr.Snapshot(ctx, candidate)
		if err != nil {
			cand.Cleanup()
			_ = finishAll(sqlite.RunError, err.Error(), false)
			return err
		}
		outcome, err := e.judgeCohort(ctx, job, dir, candidate, evidence, fence)
		cleanup()
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
			break
		}
		err = mgr.Promote(ctx, job.proj.TargetBranch, cand)
		cand.Cleanup()
		if err == nil {
			break
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
			}
			return tx.FinishJob(job.id, job.owner.Digest(), "error", candidate, err.Error(), now)
		})
		return err
	}
	now := e.now()
	// Finalise atomically with the same rechecks as single-task completion.
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		if err := fence(tx); err != nil {
			return err
		}
		proj, err := tx.GetProject(job.proj.ID)
		if err != nil {
			return err
		}
		if (verification.Policy{Regression: proj.Regression}).Digest() != job.regressionD {
			return fault.New(fault.CodePlanConflict, "regression policy changed during cohort verification")
		}
		for _, m := range job.members {
			r, err := tx.GetRecord(m.rec.Task.ID)
			if err != nil {
				return err
			}
			switch {
			case r.UnmetRequires > 0:
				return fault.New(fault.CodeTaskBlocked, "%s has incomplete prerequisites", r.Task.ID)
			case r.Task.ContractRev != m.contractRev:
				return fault.New(fault.CodePlanConflict, "%s contract changed during cohort verification", r.Task.ID)
			case r.Task.Archived():
				return fault.New(fault.CodePlanConflict, "%s was archived during cohort verification", r.Task.ID)
			case r.Submission == nil || r.Submission.ID != m.submission:
				return fault.New(fault.CodePlanConflict, "%s was resubmitted during cohort verification", r.Task.ID)
			}
			if v := verification.Judge(m.policy, evidence[m.runID]); !v.Passed {
				return fault.New(fault.CodeVerificationFailed, "%s: %s", r.Task.ID, v.Summary)
			}
		}
		for _, m := range job.members {
			if err := tx.FinishRun(m.runID, sqlite.RunPassed, "cohort verified on candidate "+short(candidate), now); err != nil {
				return err
			}
			if candidate != "" && candidate != m.revision {
				_ = tx.SetRunIntegratedRevision(m.runID, candidate)
			}
			if err := tx.MarkComplete(m.rec.Task.ID, m.submission, now); err != nil {
				return err
			}
			if job.proj.Integration == project.IntegrationPromote {
				if err := tx.InsertIntegration(proj.ID, m.rec.Task.ID, job.id, proj.TargetBranch, cand.Base, m.revision, candidate, now); err != nil {
					return err
				}
			}
		}
		return tx.FinishJob(job.id, job.owner.Digest(), "passed", candidate, "cohort verified", now)
	})
	if err != nil {
		code := fault.CodeOf(err)
		if code != fault.CodeInternal {
			_ = finishAll(sqlite.RunFailed, "cohort finalisation refused: "+err.Error(), false)
		}
		return err
	}
	return nil
}

// jobHeartbeat renews the verifier's job lease while checks run.
func (e *Engine) jobHeartbeat(ctx context.Context, job cohortJob) (stop func()) {
	hctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(execution.HeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-hctx.Done():
				return
			case <-t.C:
				err := e.store.Write(hctx, func(tx *sqlite.Tx) error {
					return tx.RenewJob(job.id, job.owner.Digest(), e.now().Add(execution.DefaultLease))
				})
				if err != nil {
					return
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}

// judgeCohort runs every member's task checks and the shared regression
// suite on dir (the candidate) and records per-member verdicts. It returns
// true when every member passed; otherwise it records failures (sending
// failing members back for repair) and returns false.
func (e *Engine) judgeCohort(ctx context.Context, job cohortJob, dir, candidate string, evidence map[int64][]verification.Evidence, fence func(*sqlite.Tx) error) (bool, error) {
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
		ev, err := e.runChecks(ctx, dir, verification.Policy{TaskChecks: m.policy.TaskChecks}, m.runID, candidate, fence)
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
		ev, err := e.runChecks(ctx, dir, verification.Policy{Regression: job.proj.Regression}, first.runID, candidate, fence)
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
				// No blame can be assigned: every member is sent back.
				if err := tx.FinishRun(m.runID, sqlite.RunFailed, "cohort regression failed on candidate "+short(candidate)+": "+regressionSummary, now); err != nil {
					return err
				}
				if err := tx.RecordFailure(m.rec.Task.ID, job.proj.RetryCooldown, now); err != nil {
					return err
				}
			case verdicts[m.runID].Summary != "":
				if err := tx.FinishRun(m.runID, sqlite.RunFailed, "task checks failed on cohort candidate "+short(candidate)+": "+verdicts[m.runID].Summary, now); err != nil {
					return err
				}
				if err := tx.RecordFailure(m.rec.Task.ID, job.proj.RetryCooldown, now); err != nil {
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
