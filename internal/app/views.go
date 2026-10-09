package app

import (
	"time"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TaskSummary is the list-row read model.
type TaskSummary struct {
	ID                task.ID     `json:"id"`
	Description       string      `json:"description"`
	Status            task.Status `json:"status"`
	Manual            bool        `json:"manual,omitempty"`
	UnmetDependencies int         `json:"unmet_dependencies"`
	AttemptSeq        int         `json:"attempt,omitempty"`
	LeaseExpiresAt    *time.Time  `json:"lease_expires_at,omitempty"`
	CompletedAt       *time.Time  `json:"completed_at,omitempty"`
}

// AttemptView describes the current execution attempt without its token.
type AttemptView struct {
	Seq            int                 `json:"seq"`
	StartedAt      time.Time           `json:"started_at"`
	LeaseExpiresAt time.Time           `json:"lease_expires_at"`
	LeaseActive    bool                `json:"lease_active"`
	EndedAt        *time.Time          `json:"ended_at,omitempty"`
	EndReason      execution.EndReason `json:"end_reason,omitempty"`
}

// SubmissionView describes a submission and its newest verification run.
type SubmissionView struct {
	ID           int64            `json:"id"`
	AttemptSeq   int              `json:"attempt"`
	Revision     string           `json:"revision,omitempty"`
	SubmittedAt  time.Time        `json:"submitted_at"`
	Verification sqlite.RunStatus `json:"verification"`
	RunID        int64            `json:"run_id,omitempty"`
	PolicyDigest string           `json:"policy_digest,omitempty"`
	Summary      string           `json:"summary,omitempty"`
	FinishedAt   *time.Time       `json:"verification_finished_at,omitempty"`
}

// TaskView is the full read model returned by Show and embedded in Session.
type TaskView struct {
	Task         task.Task          `json:"task"`
	Status       task.Status        `json:"status"`
	Requires     []TaskSummary      `json:"requires,omitempty"`
	RequiredBy   []TaskSummary      `json:"required_by,omitempty"`
	Attempt      *AttemptView       `json:"attempt,omitempty"`
	Submission   *SubmissionView    `json:"submission,omitempty"`
	Handoff      *execution.Handoff `json:"handoff,omitempty"`
	CompletedAt  *time.Time         `json:"completed_at,omitempty"`
	PolicyDigest string             `json:"policy_digest"`
}

// Session is what Take returns: the only place a token is ever emitted.
type Session struct {
	TaskID     task.ID           `json:"task_id"`
	Token      execution.Token   `json:"token"`
	AttemptSeq int               `json:"attempt"`
	ExpiresAt  time.Time         `json:"lease_expires_at"`
	Resumed    bool              `json:"resumed"`
	Task       TaskView          `json:"task"`
	Handoff    execution.Handoff `json:"handoff"`
}

// SubmissionResult is what Finish returns.
type SubmissionResult struct {
	TaskID           task.ID                 `json:"task_id"`
	SubmissionID     int64                   `json:"submission_id"`
	AttemptSeq       int                     `json:"attempt"`
	Revision         string                  `json:"revision,omitempty"`
	RunID            int64                   `json:"run_id,omitempty"`
	PolicyDigest     string                  `json:"policy_digest,omitempty"`
	Verification     sqlite.RunStatus        `json:"verification"`
	Summary          string                  `json:"summary,omitempty"`
	Status           task.Status             `json:"status"`
	Evidence         []verification.Evidence `json:"evidence,omitempty"`
	AlreadySubmitted bool                    `json:"already_submitted,omitempty"`
	Message          string                  `json:"message,omitempty"`
}

// HistoryPage is a page of a task's log.
type HistoryPage struct {
	TaskID      task.ID                 `json:"task_id"`
	Total       int                     `json:"total"`
	Offset      int                     `json:"offset"`
	Entries     []execution.RecordedLog `json:"entries"`
	Attempts    []AttemptView           `json:"attempts,omitempty"`
	Submissions []SubmissionView        `json:"submissions,omitempty"`
}

func summarize(r sqlite.Record, now time.Time) TaskSummary {
	s := TaskSummary{ID: r.Task.ID, Description: r.Task.Description, Status: r.Status(now), Manual: r.Task.Manual, UnmetDependencies: r.UnmetDependencies, CompletedAt: r.CompletedAt}
	if a := r.Attempt; a != nil && a.LeaseActive(now) {
		s.AttemptSeq = a.Seq
		exp := a.LeaseExpiresAt
		s.LeaseExpiresAt = &exp
	}
	return s
}

func attemptView(a *execution.Attempt, now time.Time) *AttemptView {
	if a == nil {
		return nil
	}
	return &AttemptView{Seq: a.Seq, StartedAt: a.StartedAt, LeaseExpiresAt: a.LeaseExpiresAt, LeaseActive: a.LeaseActive(now), EndedAt: a.EndedAt, EndReason: a.EndReason}
}

func submissionView(s *sqlite.Submission) *SubmissionView {
	if s == nil {
		return nil
	}
	v := &SubmissionView{ID: s.ID, AttemptSeq: s.AttemptSeq, Revision: s.Revision, SubmittedAt: s.SubmittedAt, Verification: s.RunStatus()}
	if r := s.Run; r != nil {
		v.RunID, v.PolicyDigest, v.Summary, v.FinishedAt = r.ID, r.PolicyDigest, r.Summary, r.FinishedAt
	}
	return v
}

// buildView assembles a TaskView inside a transaction.
func (e *Engine) buildView(tx *sqlite.Tx, r sqlite.Record, now time.Time) (TaskView, error) {
	v := TaskView{Task: r.Task, Status: r.Status(now), Attempt: attemptView(r.Attempt, now), Submission: submissionView(r.Submission), CompletedAt: r.CompletedAt, PolicyDigest: r.Task.Verification.Digest()}
	if err := tx.LoadAcceptance(&v.Task); err != nil {
		return v, err
	}
	reqs, err := tx.Requirements(r.Task.ID)
	if err != nil {
		return v, err
	}
	for _, d := range reqs {
		v.Requires = append(v.Requires, summarize(d, now))
	}
	deps, err := tx.Dependents(r.Task.ID)
	if err != nil {
		return v, err
	}
	for _, d := range deps {
		v.RequiredBy = append(v.RequiredBy, summarize(d, now))
	}
	h, err := tx.Handoff(r.Task.ID)
	if err != nil {
		return v, err
	}
	v.Handoff = &h
	return v, nil
}
