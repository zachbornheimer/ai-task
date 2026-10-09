package app

import (
	"time"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// Rel is a related task as shown in views.
type Rel struct {
	ID     task.ID     `json:"id"`
	Key    string      `json:"key,omitempty"`
	Title  string      `json:"title"`
	Status task.Status `json:"status"`
}

// Progress is a group's derived member completion.
type Progress struct {
	Complete int `json:"complete"`
	Total    int `json:"total"`
}

// AttemptView describes an execution attempt without its token.
type AttemptView struct {
	Seq            int                 `json:"seq"`
	StartedAt      time.Time           `json:"started_at"`
	LeaseExpiresAt time.Time           `json:"lease_expires_at"`
	LeaseActive    bool                `json:"lease_active"`
	EndedAt        *time.Time          `json:"ended_at,omitempty"`
	EndReason      execution.EndReason `json:"end_reason,omitempty"`
	Workspace      string              `json:"workspace,omitempty"`
	Branch         string              `json:"branch,omitempty"`
}

// SubmissionView describes a submission and its newest run.
type SubmissionView struct {
	ID           int64            `json:"id"`
	AttemptSeq   int              `json:"attempt"`
	Revision     string           `json:"revision,omitempty"`
	Cohort       string           `json:"cohort,omitempty"`
	SubmittedAt  time.Time        `json:"submitted_at"`
	Verification sqlite.RunStatus `json:"verification"`
	RunID        int64            `json:"run_id,omitempty"`
	Summary      string           `json:"summary,omitempty"`
}

// RunView is a verification run with its evidence.
type RunView struct {
	ID                 int64                   `json:"id"`
	Mode               verification.Mode       `json:"mode"`
	Status             sqlite.RunStatus        `json:"status"`
	AttemptSeq         int                     `json:"attempt,omitempty"`
	SubmissionID       int64                   `json:"submission_id,omitempty"`
	JobID              int64                   `json:"job_id,omitempty"`
	Revision           string                  `json:"revision,omitempty"`
	IntegratedRevision string                  `json:"integrated_revision,omitempty"`
	PolicyDigest       string                  `json:"policy_digest"`
	Environment        string                  `json:"environment,omitempty"`
	StartedAt          *time.Time              `json:"started_at,omitempty"`
	FinishedAt         *time.Time              `json:"finished_at,omitempty"`
	Summary            string                  `json:"summary,omitempty"`
	Evidence           []verification.Evidence `json:"evidence,omitempty"`
}

// VerificationSummary shows the latest run of each kind.
type VerificationSummary struct {
	Task       *RunView `json:"task,omitempty"`
	Regression *RunView `json:"regression,omitempty"`
	Complete   *RunView `json:"complete,omitempty"`
}

// History is the opt-in full record of a task.
type History struct {
	Logs         []execution.RecordedLog `json:"logs"`
	Attempts     []AttemptView           `json:"attempts"`
	Submissions  []SubmissionView        `json:"submissions"`
	Runs         []RunView               `json:"runs"`
	Integrations []sqlite.Integration    `json:"integrations"`
}

// TaskView is the read model for one task or group.
type TaskView struct {
	ID                task.ID                    `json:"id"`
	Kind              task.Kind                  `json:"kind"`
	Key               string                     `json:"key,omitempty"`
	Project           string                     `json:"project,omitempty"`
	Title             string                     `json:"title"`
	Outcome           string                     `json:"outcome,omitempty"`
	Constraints       []string                   `json:"constraints,omitempty"`
	Acceptance        []task.AcceptanceCriterion `json:"acceptance,omitempty"`
	Checks            []verification.CheckSpec   `json:"task_checks,omitempty"`
	Cohort            string                     `json:"cohort,omitempty"`
	ContractRev       int                        `json:"contract_rev"`
	Status            task.Status                `json:"status"`
	Group             *Rel                       `json:"group,omitempty"`
	Children          []Rel                      `json:"children,omitempty"`
	Progress          *Progress                  `json:"progress,omitempty"`
	Requires          []Rel                      `json:"requires,omitempty"`
	Blocks            []Rel                      `json:"blocks,omitempty"`
	Attempt           *AttemptView               `json:"attempt,omitempty"`
	Submission        *SubmissionView            `json:"submission,omitempty"`
	Verification      VerificationSummary        `json:"verification"`
	Handoff           *execution.Handoff         `json:"handoff,omitempty"`
	Failures          int                        `json:"failures,omitempty"`
	NextEligibleAt    *time.Time                 `json:"next_eligible_at,omitempty"`
	CompletedAt       *time.Time                 `json:"completed_at,omitempty"`
	CompletedRevision string                     `json:"completed_revision,omitempty"`
	ArchivedAt        *time.Time                 `json:"archived_at,omitempty"`
	CreatedAt         time.Time                  `json:"created_at"`
	History           *History                   `json:"history,omitempty"`
}

// GroupView is a group with derived progress.
type GroupView struct {
	ID       task.ID  `json:"id"`
	Key      string   `json:"key,omitempty"`
	Title    string   `json:"title"`
	ParentID task.ID  `json:"parent_id,omitempty"`
	Progress Progress `json:"progress"`
	// Complete is derived: a nonempty member set that is fully complete.
	Complete bool `json:"complete"`
}

// Summary is the project-level view a driver needs.
type Summary struct {
	ProjectID project.ID          `json:"project_id"`
	PlanRev   uint64              `json:"plan_rev"`
	Total     int                 `json:"total"`
	Open      int                 `json:"open"`
	Counts    map[task.Status]int `json:"counts"`
	// Claimable counts tasks a Claim could take now.
	Claimable int `json:"claimable"`
	// Active counts work in flight that can still make progress: live
	// claims, running verification, and cohort submissions whose
	// verification can run.
	Active int `json:"active"`
	// PendingCohorts names cohorts whose members have all submitted and
	// whose verification job can run (or is running).
	PendingCohorts []string `json:"pending_cohorts,omitempty"`
	// Done: every executable task is complete (groups and archived tasks
	// do not count).
	Done bool `json:"done"`
	// Stalled: open tasks remain, nothing is claimable, nothing is active.
	Stalled bool `json:"stalled"`
	// Reasons explains a stall for humans.
	Reasons []string `json:"reasons,omitempty"`
}

// PlanSnapshot is the whole current plan.
type PlanSnapshot struct {
	Revision uint64      `json:"plan_rev"`
	Tasks    []TaskView  `json:"tasks"`
	Groups   []GroupView `json:"groups"`
	Summary  Summary     `json:"summary"`
}

// Session is what Claim returns: the only place a token is ever emitted.
type Session struct {
	Task       TaskView          `json:"task"`
	Token      execution.Token   `json:"token"`
	AttemptSeq int               `json:"attempt"`
	LeaseUntil time.Time         `json:"lease_until"`
	Workspace  string            `json:"workspace,omitempty"`
	Branch     string            `json:"branch,omitempty"`
	Resumed    bool              `json:"resumed"`
	Handoff    execution.Handoff `json:"handoff"`
}

// VerifyResult reports one verification invocation.
type VerifyResult struct {
	TaskID             task.ID                 `json:"task_id"`
	Mode               verification.Mode       `json:"mode"`
	RunID              int64                   `json:"run_id,omitempty"`
	Revision           string                  `json:"revision,omitempty"`
	IntegratedRevision string                  `json:"integrated_revision,omitempty"`
	Passed             bool                    `json:"passed"`
	Completed          bool                    `json:"completed"`
	Submitted          bool                    `json:"submitted,omitempty"`
	SubmissionID       int64                   `json:"submission_id,omitempty"`
	Status             task.Status             `json:"status"`
	Summary            string                  `json:"summary"`
	Evidence           []verification.Evidence `json:"evidence"`
	Message            string                  `json:"message,omitempty"`
	Replayed           bool                    `json:"replayed,omitempty"`
}

func rel(r sqlite.Record, st task.Status) Rel {
	return Rel{ID: r.Task.ID, Key: r.Task.Key, Title: r.Task.Description, Status: st}
}

func attemptView(a *execution.Attempt, now time.Time) *AttemptView {
	if a == nil {
		return nil
	}
	return &AttemptView{Seq: a.Seq, StartedAt: a.StartedAt, LeaseExpiresAt: a.LeaseExpiresAt, LeaseActive: a.LeaseActive(now), EndedAt: a.EndedAt, EndReason: a.EndReason, Workspace: a.WorkspacePath, Branch: a.WorkspaceBranch}
}

func submissionView(s *sqlite.Submission) *SubmissionView {
	if s == nil {
		return nil
	}
	v := &SubmissionView{ID: s.ID, AttemptSeq: s.AttemptSeq, Revision: s.Revision, Cohort: s.Cohort, SubmittedAt: s.SubmittedAt, Verification: s.RunStatus()}
	if r := s.Run; r != nil {
		v.RunID, v.Summary = r.ID, r.Summary
	}
	return v
}

func runView(r sqlite.Run, attemptSeq int, evidence []verification.Evidence) RunView {
	return RunView{ID: r.ID, Mode: r.Mode, Status: r.Status, AttemptSeq: attemptSeq, SubmissionID: r.SubmissionID, JobID: r.JobID, Revision: r.Revision, IntegratedRevision: r.IntegratedRevision, PolicyDigest: r.PolicyDigest, Environment: r.Environment, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Summary: r.Summary, Evidence: evidence}
}

// viewDepth selects how much of a task to load.
type viewDepth int

const (
	// viewList: the row a listing needs: identity, status, group, and the
	// prerequisites of a blocked task. One or two extra queries per row.
	viewList viewDepth = iota
	// viewShow: everything a detail view shows, bounded handoff included.
	viewShow
	// viewFull: viewShow plus full history, runs and evidence.
	viewFull
)

// buildView assembles a TaskView inside a transaction.
func (e *Engine) buildView(tx *sqlite.Tx, r sqlite.Record, p project.Project, now time.Time, full bool) (TaskView, error) {
	depth := viewShow
	if full {
		depth = viewFull
	}
	return e.buildViewDepth(tx, r, p, now, depth)
}

// relFor loads the light relation row for a neighbour.
func relFor(tx *sqlite.Tx, id task.ID, p project.Project, now time.Time) (Rel, bool, error) {
	n, err := tx.GetRecord(id)
	if err != nil {
		return Rel{}, false, err
	}
	if n.Task.Archived() {
		return Rel{}, false, nil
	}
	return rel(n, n.Status(now, p.MaxAttempts)), true, nil
}

func (e *Engine) buildViewDepth(tx *sqlite.Tx, r sqlite.Record, p project.Project, now time.Time, depth viewDepth) (TaskView, error) {
	st := r.Status(now, p.MaxAttempts)
	t := r.Task
	v := TaskView{
		ID: t.ID, Kind: t.Kind, Key: t.Key, Project: p.Name, Title: t.Description, Outcome: t.Outcome, Constraints: t.Constraints,
		Checks: t.Verification.TaskChecks, Cohort: t.Cohort, ContractRev: t.ContractRev, Status: st,
		Attempt: attemptView(r.Attempt, now), Submission: submissionView(r.Submission),
		Failures: r.Failures, CompletedAt: r.CompletedAt, CompletedRevision: r.CompletedRevision, ArchivedAt: t.ArchivedAt, CreatedAt: t.CreatedAt,
	}
	if !r.NextEligibleAt.IsZero() && r.NextEligibleAt.After(now) {
		ne := r.NextEligibleAt
		v.NextEligibleAt = &ne
	}
	if t.ParentID != "" {
		if g, err := tx.GetRecord(t.ParentID); err == nil {
			gr := rel(g, task.StatusGroup)
			v.Group = &gr
		}
	}
	if depth == viewList {
		if t.Kind == task.KindTask && st == task.StatusBlocked {
			ids, err := tx.RequirementIDs(t.ID)
			if err != nil {
				return v, err
			}
			for _, id := range ids {
				rr, ok, err := relFor(tx, id, p, now)
				if err != nil {
					return v, err
				}
				if ok {
					v.Requires = append(v.Requires, rr)
				}
			}
		}
		return v, nil
	}
	if err := tx.LoadAcceptance(&t); err != nil {
		return v, err
	}
	v.Acceptance = t.Acceptance
	if t.Kind == task.KindGroup {
		children, err := tx.Children(t.ID)
		if err != nil {
			return v, err
		}
		prog := &Progress{}
		for _, c := range children {
			cs := c.Status(now, p.MaxAttempts)
			v.Children = append(v.Children, rel(c, cs))
			if c.Task.Kind == task.KindTask {
				prog.Total++
				if cs == task.StatusComplete {
					prog.Complete++
				}
			} else {
				gp, err := e.groupProgress(tx, c.Task.ID, p, now)
				if err != nil {
					return v, err
				}
				prog.Total += gp.Total
				prog.Complete += gp.Complete
			}
		}
		v.Progress = prog
		return v, nil
	}
	reqs, err := tx.Requirements(t.ID)
	if err != nil {
		return v, err
	}
	for _, d := range reqs {
		if d.Task.Archived() {
			continue
		}
		v.Requires = append(v.Requires, rel(d, d.Status(now, p.MaxAttempts)))
	}
	deps, err := tx.Dependents(t.ID)
	if err != nil {
		return v, err
	}
	for _, d := range deps {
		if d.Task.Archived() {
			continue
		}
		v.Blocks = append(v.Blocks, rel(d, d.Status(now, p.MaxAttempts)))
	}
	h, err := tx.Handoff(t.ID)
	if err != nil {
		return v, err
	}
	v.Handoff = &h
	runs, err := tx.RunsForTask(t.ID)
	if err != nil {
		return v, err
	}
	seqOf := map[int64]int{}
	attempts, err := tx.Attempts(t.ID)
	if err != nil {
		return v, err
	}
	for _, a := range attempts {
		seqOf[a.ID] = a.Seq
	}
	for _, run := range runs {
		rv := runView(run, seqOf[run.AttemptID], nil)
		switch run.Mode {
		case verification.ModeTask:
			v.Verification.Task = &rv
		case verification.ModeRegression:
			v.Verification.Regression = &rv
		default:
			v.Verification.Complete = &rv
		}
	}
	if depth == viewFull {
		hist := &History{Logs: []execution.RecordedLog{}, Attempts: []AttemptView{}, Submissions: []SubmissionView{}, Runs: []RunView{}, Integrations: []sqlite.Integration{}}
		if hist.Logs, err = tx.Logs(t.ID, 10000, 0); err != nil {
			return v, err
		}
		for i := range attempts {
			hist.Attempts = append(hist.Attempts, *attemptView(&attempts[i], now))
		}
		subs, err := tx.Submissions(t.ID)
		if err != nil {
			return v, err
		}
		for i := range subs {
			hist.Submissions = append(hist.Submissions, *submissionView(&subs[i]))
		}
		for _, run := range runs {
			ev, err := tx.EvidenceForRun(run.ID)
			if err != nil {
				return v, err
			}
			hist.Runs = append(hist.Runs, runView(run, seqOf[run.AttemptID], ev))
		}
		if hist.Integrations, err = tx.IntegrationsForTask(t.ID); err != nil {
			return v, err
		}
		if hist.Logs == nil {
			hist.Logs = []execution.RecordedLog{}
		}
		if hist.Integrations == nil {
			hist.Integrations = []sqlite.Integration{}
		}
		v.History = hist
	}
	return v, nil
}

// groupProgress derives a group's member completion recursively.
func (e *Engine) groupProgress(tx *sqlite.Tx, id task.ID, p project.Project, now time.Time) (Progress, error) {
	var prog Progress
	children, err := tx.Children(id)
	if err != nil {
		return prog, err
	}
	for _, c := range children {
		if c.Task.Kind == task.KindGroup {
			gp, err := e.groupProgress(tx, c.Task.ID, p, now)
			if err != nil {
				return prog, err
			}
			prog.Total += gp.Total
			prog.Complete += gp.Complete
			continue
		}
		prog.Total++
		if c.Status(now, p.MaxAttempts) == task.StatusComplete {
			prog.Complete++
		}
	}
	return prog, nil
}
