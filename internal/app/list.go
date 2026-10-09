package app

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// Filter selects which tasks a List returns.
type Filter string

const (
	// FilterOpen: live tasks and groups that are not complete (default).
	FilterOpen Filter = "open"
	// FilterReady: tasks a machine could claim now.
	FilterReady Filter = "ready"
	// FilterBlocked: tasks with unmet hard prerequisites.
	FilterBlocked Filter = "blocked"
	// FilterAll: everything live, complete included.
	FilterAll Filter = "all"
	// FilterArchived: archived tasks only.
	FilterArchived Filter = "archived"
)

// ListQuery selects a project and a filter.
type ListQuery struct {
	ProjectID project.ID
	Filter    Filter
}

// List returns a consistent snapshot of the plan.
func (e *Engine) List(ctx context.Context, q ListQuery) (PlanSnapshot, error) {
	if q.Filter == "" {
		q.Filter = FilterOpen
	}
	if err := e.reconcile(ctx); err != nil {
		return PlanSnapshot{}, err
	}
	now := e.now()
	var snap PlanSnapshot
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		proj, err := tx.GetProject(q.ProjectID)
		if err != nil {
			return err
		}
		snap.Revision = proj.PlanRev
		scope := sqlite.ScopeLive
		if q.Filter == FilterArchived {
			scope = sqlite.ScopeAll
		}
		records, err := tx.ListRecords(q.ProjectID, scope, now, proj.MaxAttempts)
		if err != nil {
			return err
		}
		snap.Tasks, snap.Groups = []TaskView{}, []GroupView{}
		for _, r := range records {
			st := r.Status(now, proj.MaxAttempts)
			if r.Task.Kind == task.KindGroup {
				if q.Filter == FilterArchived {
					continue
				}
				prog, err := e.groupProgress(tx, r.Task.ID, proj, now)
				if err != nil {
					return err
				}
				if q.Filter == FilterOpen && prog.Total > 0 && prog.Complete == prog.Total {
					continue
				}
				snap.Groups = append(snap.Groups, GroupView{ID: r.Task.ID, Key: r.Task.Key, Title: r.Task.Description, ParentID: r.Task.ParentID, Progress: prog, Complete: prog.Total > 0 && prog.Complete == prog.Total})
				continue
			}
			switch q.Filter {
			case FilterOpen:
				if !st.Open() {
					continue
				}
			case FilterReady:
				if !st.Claimable() {
					continue
				}
			case FilterBlocked:
				if st != task.StatusBlocked {
					continue
				}
			case FilterArchived:
				if st != task.StatusArchived {
					continue
				}
			}
			v, err := e.buildViewDepth(tx, r, proj, now, viewList)
			if err != nil {
				return err
			}
			snap.Tasks = append(snap.Tasks, v)
		}
		snap.Summary, err = e.summaryIn(tx, proj, now)
		return err
	})
	return snap, err
}

// Show returns the full read model for one task or group by ID or key.
func (e *Engine) Show(ctx context.Context, pid project.ID, ref string, full bool) (TaskView, error) {
	if err := e.reconcile(ctx); err != nil {
		return TaskView{}, err
	}
	now := e.now()
	var v TaskView
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		r, err := e.lookup(tx, pid, ref)
		if err != nil {
			return err
		}
		proj, err := tx.GetProject(r.Task.ProjectID)
		if err != nil {
			return err
		}
		v, err = e.buildView(tx, r, proj, now, full)
		return err
	})
	return v, err
}

// lookup resolves an ID (any project) or a key (needs a project).
func (e *Engine) lookup(tx *sqlite.Tx, pid project.ID, ref string) (sqlite.Record, error) {
	if task.IsID(ref) {
		return tx.GetRecord(task.ID(ref))
	}
	if pid == "" {
		return sqlite.Record{}, fault.New(fault.CodeNoProject, "a key needs a project; pass --project or run inside the repository")
	}
	r, ok, err := tx.GetByKey(pid, ref)
	if err != nil {
		return r, err
	}
	if !ok {
		return r, fault.New(fault.CodeNotFound, "no task with key %q", ref)
	}
	return r, nil
}

// Summary computes the project status.
func (e *Engine) Summary(ctx context.Context, pid project.ID) (Summary, error) {
	if err := e.reconcile(ctx); err != nil {
		return Summary{}, err
	}
	now := e.now()
	var s Summary
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		proj, err := tx.GetProject(pid)
		if err != nil {
			return err
		}
		s, err = e.summaryIn(tx, proj, now)
		return err
	})
	return s, err
}

func (e *Engine) summaryIn(tx *sqlite.Tx, proj project.Project, now interface{ IsZero() bool }) (Summary, error) {
	n := e.now()
	s := Summary{ProjectID: proj.ID, PlanRev: proj.PlanRev, Counts: map[task.Status]int{}}
	records, err := tx.ListRecords(proj.ID, sqlite.ScopeLive, n, proj.MaxAttempts)
	if err != nil {
		return s, err
	}
	// Cohort readiness: a cohort can be verified when every live member has
	// a submission that is pending or running.
	cohortMembers := map[string][]sqlite.Record{}
	for _, r := range records {
		if r.Task.Kind == task.KindTask && r.Task.Cohort != "" && r.CompletedAt == nil {
			cohortMembers[r.Task.Cohort] = append(cohortMembers[r.Task.Cohort], r)
		}
	}
	runnable := map[string]bool{}
	backoff := map[string]time.Time{}
	var cohortsBackingOff, cohortsStuck int
	for cohort, members := range cohortMembers {
		ok := len(members) > 0
		for _, m := range members {
			if m.Submission == nil || (m.Submission.RunStatus() != sqlite.RunPending && m.Submission.RunStatus() != sqlite.RunRunning) {
				ok = false
			}
		}
		runnable[cohort] = ok
		if ok {
			next, errs, err := cohortBackoff(tx, proj.ID, cohort)
			if err != nil {
				return s, err
			}
			if next.After(n) {
				backoff[cohort] = next
				cohortsBackingOff++
				if proj.MaxAttempts > 0 && errs >= proj.MaxAttempts {
					cohortsStuck++
				}
			}
		}
	}
	var exhausted, blockedByExhausted, workspaceBlocked, unverifiable int
	for _, r := range records {
		if r.Task.Kind != task.KindTask {
			continue
		}
		st := r.Status(n, proj.MaxAttempts)
		s.Total++
		s.Counts[st]++
		if st.Open() {
			s.Open++
		}
		switch {
		case st.Claimable():
			s.Claimable++
		case st == task.StatusCooldown:
			s.Cooling++
			if s.NextEligibleAt == nil || r.NextEligibleAt.Before(*s.NextEligibleAt) {
				ne := r.NextEligibleAt
				s.NextEligibleAt = &ne
			}
		case st == task.StatusClaimed, st == task.StatusVerifying, st == task.StatusAwaitingIntegration:
			// An open integration intent is work in flight: its owner is
			// promoting, or reconciliation will resolve it from Git once
			// the owner's lease expires.
			s.Active++
		case st == task.StatusAwaitingVerification:
			if next, ok := backoff[r.Task.Cohort]; ok {
				// The cohort's promotion is backing off: self-resolving.
				s.Cooling++
				if s.NextEligibleAt == nil || next.Before(*s.NextEligibleAt) {
					ne := next
					s.NextEligibleAt = &ne
				}
			} else if r.Task.Cohort == "" || runnable[r.Task.Cohort] {
				s.Active++
			}
		case st == task.StatusNeedsAttention:
			switch facts := r.Facts(n, proj.MaxAttempts); {
			case facts.Unverifiable:
				unverifiable++
			case facts.Exhausted:
				exhausted++
			default:
				workspaceBlocked++
			}
		case st == task.StatusBlocked:
			blockedByExhausted++
		}
	}
	for cohort, ok := range runnable {
		if ok {
			s.PendingCohorts = append(s.PendingCohorts, cohort)
		}
	}
	sort.Strings(s.PendingCohorts)
	s.Done = s.Open == 0
	s.Stalled = s.Open > 0 && s.Claimable == 0 && s.Active == 0 && s.Cooling == 0
	if s.Stalled {
		if exhausted > 0 {
			s.Reasons = append(s.Reasons, fmt.Sprintf("%d task(s) exhausted their attempts; fix and `at update <task> --reset-attempts`", exhausted))
		}
		if workspaceBlocked > 0 {
			s.Reasons = append(s.Reasons, fmt.Sprintf("%d task(s) have an unusable workspace; fix the worktree and `at claim <task>`, or `at update <task> --reset-attempts`", workspaceBlocked))
		}
		if cohortsStuck > 0 {
			s.Reasons = append(s.Reasons, fmt.Sprintf("%d cohort(s) keep failing to promote; see the members' last_error", cohortsStuck))
		}
		if unverifiable > 0 {
			s.Reasons = append(s.Reasons, fmt.Sprintf("%d task(s) have no required task check; `at update <task> --check ...`", unverifiable))
		}
		if c := s.Counts[task.StatusAwaitingVerification]; c > 0 {
			s.Reasons = append(s.Reasons, fmt.Sprintf("%d cohort member(s) await peers that cannot progress", c))
		}
		if blockedByExhausted > 0 {
			s.Reasons = append(s.Reasons, fmt.Sprintf("%d task(s) blocked behind work that cannot progress", blockedByExhausted))
		}
		if len(s.Reasons) == 0 {
			s.Reasons = append(s.Reasons, "open tasks remain but nothing is claimable or in flight")
		}
	}
	return s, nil
}

var _ = execution.DefaultLease
