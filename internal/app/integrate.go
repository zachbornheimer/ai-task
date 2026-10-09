package app

import (
	"context"
	"fmt"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

// Integration protocol
//
// Git and SQLite cannot share a transaction, so a promotion is bridged by
// a durable intent:
//
//  1. verify the immutable candidate (and re-verify after a merge that
//     changed content);
//  2. in one fenced transaction re-check everything completion depends on
//     (session or job ownership, prerequisites, pinned contract revision,
//     pinned regression digest, archive state, submission identity,
//     stored evidence for the candidate) and record an intent: the task
//     is now awaiting_integration and the planner cannot edit it;
//  3. move the target branch with a compare-and-swap, authorised by the
//     intent (never by a possibly stale token);
//  4. in a second short transaction record completion for the intent.
//
// A process that dies between 3 and 4 leaves an open intent whose owner
// lease expires; reconcileIntents then asks Git whether the candidate is
// in the target (complete deterministically) or not (abandon, the task
// becomes claimable again, nothing is blamed on the agent).

// fault is the test seam for crash injection: a non-nil error at a point
// makes the operation stop exactly there, as a killed process would.
func (e *Engine) fault(point string) error {
	if e.faults == nil {
		return nil
	}
	return e.faults(point)
}

// intentSpec is what one promotion pins.
type intentSpec struct {
	proj        project.Project
	taskID      task.ID
	attemptID   int64 // single-task
	jobID       int64 // cohort
	runID       int64
	submission  int64
	owner       string // token digest (attempt or job owner)
	contractRev int
	regression  string // digest pinned at preparation
	policy      verification.Policy
	source      string // the task's submitted revision
}

// recheckForCompletion re-reads the task inside tx and refuses when
// anything the completion depends on changed since preparation.
func (e *Engine) recheckForCompletion(tx *sqlite.Tx, s intentSpec, final string) (sqlite.Record, error) {
	r, err := tx.GetRecord(s.taskID)
	if err != nil {
		return r, err
	}
	proj, err := tx.GetProject(r.Task.ProjectID)
	if err != nil {
		return r, err
	}
	switch {
	case r.UnmetRequires > 0:
		return r, fault.New(fault.CodeTaskBlocked, "%s: a prerequisite became incomplete during verification", r.Task.ID)
	case r.Task.ContractRev != s.contractRev:
		return r, fault.New(fault.CodePlanConflict, "%s: the task contract changed during verification; verify again", r.Task.ID)
	case (verification.Policy{Regression: proj.Regression}).Digest() != s.regression:
		return r, fault.New(fault.CodePlanConflict, "%s: the project regression policy changed during verification; verify again", r.Task.ID)
	case r.Task.Archived():
		return r, fault.New(fault.CodePlanConflict, "%s was archived during verification", r.Task.ID)
	case r.Submission == nil || r.Submission.ID != s.submission:
		return r, fault.New(fault.CodePlanConflict, "%s was resubmitted during verification", r.Task.ID)
	}
	stored, err := tx.EvidenceForRun(s.runID)
	if err != nil {
		return r, err
	}
	var onFinal []verification.Evidence
	for _, ev := range stored {
		if ev.Revision == final {
			onFinal = append(onFinal, ev)
		}
	}
	if v := verification.Judge(s.policy, onFinal); !v.Passed {
		return r, fault.New(fault.CodeVerificationFailed, "stored evidence does not prove revision %s: %s", short(final), v.Summary)
	}
	return r, nil
}

// recordIntent runs the re-check and, if it holds, pins the promotion:
// the run is marked passed (the task becomes awaiting_integration) and
// the intent row is written. fence is evaluated first.
func (e *Engine) recordIntent(ctx context.Context, s intentSpec, cand workspace.Candidate, final, summary string, fence func(*sqlite.Tx) error) (int64, error) {
	now := e.now()
	var id int64
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		if err := fence(tx); err != nil {
			return err
		}
		if _, err := e.recheckForCompletion(tx, s, final); err != nil {
			return err
		}
		if err := tx.FinishRun(s.runID, sqlite.RunPassed, summary, now); err != nil {
			return err
		}
		if final != s.source {
			if err := tx.SetRunIntegratedRevision(s.runID, final); err != nil {
				return err
			}
		}
		var err error
		id, err = tx.InsertIntent(sqlite.Intent{ProjectID: s.proj.ID, TaskID: s.taskID, AttemptID: s.attemptID, JobID: s.jobID, RunID: s.runID, SubmissionID: s.submission, OwnerDigest: s.owner, ContractRev: s.contractRev, RegressionDigest: s.regression, TargetBranch: s.proj.TargetBranch, BaseRevision: cand.Base, SourceRevision: s.source, CandidateRevision: final}, now)
		return err
	})
	if err == nil {
		e.notify()
	}
	return id, err
}

// abandonIntent withdraws an intent whose promotion did not happen.
func (e *Engine) abandonIntent(ctx context.Context, id int64, summary string) {
	now := e.now()
	_ = e.store.Write(ctx, func(tx *sqlite.Tx) error { return tx.FinishIntent(id, sqlite.IntentAbandoned, summary, now) })
	e.notify()
}

// completeIntentTx records completion for one intent whose promotion
// happened. Authority is the intent itself (plus owner when given): the
// target already carries the candidate, so completion is the only
// consistent outcome. The attempt may already have ended (a release that
// raced the promotion); that does not change the outcome.
func (e *Engine) completeIntentTx(tx *sqlite.Tx, in sqlite.Intent, owner string, summary string) error {
	now := e.now()
	if owner != "" && in.OwnerDigest != owner {
		return fault.New(fault.CodeSessionSuperseded, "integration intent %d is owned by another verifier", in.ID)
	}
	if err := tx.FinishIntent(in.ID, sqlite.IntentCompleted, summary, now); err != nil {
		return err
	}
	if err := tx.MarkComplete(in.TaskID, in.SubmissionID, now); err != nil {
		return err
	}
	if in.AttemptID != 0 {
		if err := tx.EndAttempt(in.AttemptID, execution.EndFinished, now); err != nil && fault.CodeOf(err) != fault.CodeSessionFinished {
			return err
		}
	}
	return tx.InsertIntegration(in.ProjectID, in.TaskID, in.JobID, in.TargetBranch, in.BaseRevision, in.SourceRevision, in.CandidateRevision, now)
}

// reconcileIntents resolves open intents whose owner is gone by asking
// Git whether the promotion happened. It never runs Git inside a
// transaction and never blames the agent for an interrupted integration.
func (e *Engine) reconcileIntents(ctx context.Context) error {
	now := e.now()
	var orphans []sqlite.Intent
	if err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		var err error
		orphans, err = tx.OrphanedIntents(now)
		return err
	}); err != nil {
		return err
	}
	byJob := map[int64]bool{}
	for _, in := range orphans {
		proj, err := e.Project(ctx, in.ProjectID)
		if err != nil {
			return err
		}
		promoted, err := e.manager(proj).Contains(ctx, in.TargetBranch, in.CandidateRevision)
		if err != nil {
			// Git unreachable right now: leave the intent for the next pass.
			continue
		}
		in := in
		err = e.store.Write(ctx, func(tx *sqlite.Tx) error {
			cur, err := tx.GetIntent(in.ID)
			if err != nil || cur.Status != sqlite.IntentIntended {
				return err // already resolved by another process
			}
			if promoted {
				if err := e.completeIntentTx(tx, cur, "", "promotion found in "+cur.TargetBranch+" after the verifier died; completed by reconciliation"); err != nil {
					return err
				}
				if cur.JobID != 0 && !byJob[cur.JobID] {
					byJob[cur.JobID] = true
					return tx.SetJobStatus(cur.JobID, "passed", cur.CandidateRevision, "cohort promoted; completed by reconciliation", now)
				}
				return nil
			}
			if err := tx.FinishIntent(cur.ID, sqlite.IntentAbandoned, "verifier died before promoting; the candidate is not in "+cur.TargetBranch, now); err != nil {
				return err
			}
			if cur.JobID != 0 {
				// The member's submission stands; a new job rebuilds the candidate.
				if err := tx.FinishRun(cur.RunID, sqlite.RunPending, "integration interrupted; waiting for a new verifier", now); err != nil {
					return err
				}
				if !byJob[cur.JobID] {
					byJob[cur.JobID] = true
					return tx.SetJobStatus(cur.JobID, "interrupted", cur.CandidateRevision, "verifier died before promoting; job will be retried", now)
				}
				return nil
			}
			// Single task: the run did not integrate; the task is claimable
			// again with its evidence intact and no failure counted.
			return tx.FinishRun(cur.RunID, sqlite.RunError, "integration interrupted before promotion (verifier died); verify again", now)
		})
		if err != nil {
			return fault.Wrap(err, fault.CodeInternal, "reconcile integration intent %d", in.ID)
		}
		e.notify()
	}
	return nil
}

// IntentView is the read model of an integration intent.
type IntentView struct {
	ID        int64  `json:"id"`
	Status    string `json:"status"`
	Target    string `json:"target_branch"`
	Base      string `json:"base_revision"`
	Candidate string `json:"candidate_revision"`
	Summary   string `json:"summary,omitempty"`
}

func intentView(in sqlite.Intent) IntentView {
	return IntentView{ID: in.ID, Status: in.Status, Target: in.TargetBranch, Base: in.BaseRevision, Candidate: in.CandidateRevision, Summary: in.Summary}
}

var _ = fmt.Sprintf
