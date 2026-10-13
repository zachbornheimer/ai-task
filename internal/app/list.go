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
	// FilterDraft: tasks still missing a proof or regression proof.
	FilterDraft Filter = "draft"
	// FilterAll: everything live, complete included.
	FilterAll Filter = "all"
	// FilterComplete: completed live tasks and groups.
	FilterComplete Filter = "complete"
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
	heads := map[project.ID]targetHead{}
	if q.ProjectID != "" {
		heads[q.ProjectID] = e.resolveTargetHead(ctx, q.ProjectID)
	} else {
		var err error
		heads, err = e.projectHeads(ctx)
		if err != nil {
			return PlanSnapshot{}, err
		}
	}
	var snap PlanSnapshot
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		var proj project.Project
		byID := map[project.ID]project.Project{}
		if q.ProjectID != "" {
			p, err := tx.GetProject(q.ProjectID)
			if err != nil {
				return err
			}
			proj = p
			snap.Revision = proj.PlanRev
		} else {
			proj = project.Project{MaxAttempts: project.DefaultMaxAttempts, Integration: project.IntegrationNone}
			ps, err := tx.ListProjects()
			if err != nil {
				return err
			}
			for i := range ps {
				byID[ps[i].ID] = ps[i]
			}
		}
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
			rp := proj
			if q.ProjectID == "" {
				if p, ok := byID[r.Task.ProjectID]; ok {
					rp = p
				}
				if rp.MaxAttempts == 0 {
					rp.MaxAttempts = project.DefaultMaxAttempts
				}
			}
			st := r.Status(now, rp.MaxAttempts)
			if r.Task.Kind == task.KindGroup {
				if (q.Filter == FilterArchived) != r.Task.Archived() {
					continue
				}
				prog, err := e.groupProgress(tx, r.Task.ID, rp, now)
				if err != nil {
					return err
				}
				g := GroupView{ID: r.Task.ID, Key: r.Task.Key, Title: r.Task.Description, ParentID: r.Task.ParentID, Progress: prog, Complete: prog.Total > 0 && prog.Complete == prog.Total, ArchivedAt: r.Task.ArchivedAt, LastError: r.LastError}
				if verification.HasRequired(r.Task.Verification.TaskChecks) {
					gv, err := e.groupVerification(tx, r, rp, now, heads[rp.ID])
					if err != nil {
						return err
					}
					g.Checks, g.Size, g.Verification = r.Task.Verification.TaskChecks, r.Task.Size, &gv
					g.Complete = r.CompletedAt != nil
				}
				if q.Filter == FilterOpen && g.Complete {
					continue
				}
				if q.Filter == FilterComplete && !g.Complete {
					continue
				}
				snap.Groups = append(snap.Groups, g)
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
			case FilterDraft:
				if st != task.StatusDraft {
					continue
				}
			case FilterComplete:
				if st != task.StatusComplete {
					continue
				}
			case FilterArchived:
				if st != task.StatusArchived {
					continue
				}
			}
			v, err := e.buildViewDepth(tx, r, rp, now, viewList, heads[rp.ID])
			if err != nil {
				return err
			}
			snap.Tasks = append(snap.Tasks, v)
		}
		if q.ProjectID == "" {
			return nil
		}
		snap.Summary, err = e.summaryIn(tx, proj, now, heads, nil)
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
	// Groups with checks compare against the target head, which is
	// resolved before the read opens; a task view never consults it.
	var head targetHead
	_ = e.store.Read(ctx, func(tx *sqlite.Tx) error {
		if r, err := e.lookup(tx, pid, ref); err == nil && r.Task.Kind == task.KindGroup {
			head = e.resolveTargetHead(ctx, r.Task.ProjectID)
		}
		return nil
	})
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
		v, err = e.buildView(tx, r, proj, now, full, head)
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
		return sqlite.Record{}, fault.New(fault.CodeNotFound, "a key needs a project; pass --project or run inside the repository")
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

// Summary computes status. An empty pid is the whole board.
func (e *Engine) Summary(ctx context.Context, pid project.ID) (Summary, error) {
	if err := e.reconcile(ctx); err != nil {
		return Summary{}, err
	}
	now := e.now()
	heads := map[project.ID]targetHead{}
	if pid != "" {
		heads[pid] = e.resolveTargetHead(ctx, pid)
	} else {
		var err error
		heads, err = e.projectHeads(ctx)
		if err != nil {
			return Summary{}, err
		}
	}
	var s Summary
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		var proj project.Project
		var byID map[project.ID]project.Project
		var err error
		if pid == "" {
			proj = project.Project{Name: project.GeneralName, MaxAttempts: project.DefaultMaxAttempts, Integration: project.IntegrationNone}
			ps, err := tx.ListProjects()
			if err != nil {
				return err
			}
			byID = map[project.ID]project.Project{}
			for i := range ps {
				byID[ps[i].ID] = ps[i]
			}
		} else {
			proj, err = tx.GetProject(pid)
			if err != nil {
				return err
			}
		}
		s, err = e.summaryIn(tx, proj, now, heads, byID)
		return err
	})
	return s, err
}

func (e *Engine) summaryIn(tx *sqlite.Tx, proj project.Project, now interface{ IsZero() bool }, heads map[project.ID]targetHead, byID map[project.ID]project.Project) (Summary, error) {
	n := e.now()
	s := Summary{ProjectID: proj.ID, PlanRev: proj.PlanRev, Counts: map[task.Status]int{}}
	records, err := tx.ListRecords(proj.ID, sqlite.ScopeLive, n, proj.MaxAttempts)
	if err != nil {
		return s, err
	}
	var exhausted, blockedByExhausted, workspaceBlocked, drafts int
	var epicsPending, epicsFailed, epicsErrored int
	var epicErrors []string
	for _, r := range records {
		if r.Task.Kind == task.KindGroup && verification.HasRequired(r.Task.Verification.TaskChecks) && r.CompletedAt == nil {
			rp := proj
			if p, ok := byID[r.Task.ProjectID]; ok {
				rp = p
			}
			gv, err := e.groupVerification(tx, r, rp, n, heads[rp.ID])
			if err != nil {
				return s, err
			}
			switch gv.Status {
			case "pending", "running":
				epicsPending++
				if gv.NextRetry != nil {
					s.Cooling++
					if s.NextEligibleAt == nil || gv.NextRetry.Before(*s.NextEligibleAt) {
						ne := *gv.NextRetry
						s.NextEligibleAt = &ne
					}
				} else {
					s.Active++
				}
			case "failed":
				epicsFailed++
			case "error":
				epicsErrored++
				epicErrors = append(epicErrors, fmt.Sprintf("epic %s: %s", r.Task.Description, gv.Summary))
			}
			continue
		}
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
			s.Active++
		case st == task.StatusNeedsAttention:
			switch facts := r.Facts(n, proj.MaxAttempts); {
			case facts.Exhausted:
				exhausted++
			default:
				workspaceBlocked++
			}
		case st == task.StatusDraft:
			drafts++
		case st == task.StatusBlocked:
			blockedByExhausted++
		}
	}
	s.PendingEpics = epicsPending
	s.Done = s.Open == 0 && epicsPending == 0 && epicsFailed == 0 && epicsErrored == 0
	s.Stalled = (s.Open > 0 || epicsFailed > 0 || epicsErrored > 0) && s.Claimable == 0 && s.Active == 0 && s.Cooling == 0
	if s.Stalled {
		if epicsFailed > 0 {
			s.Reasons = append(s.Reasons, fmt.Sprintf("%d epic(s) failed their own checks on the current target; see the group's last_error, then add follow-up member tasks or fix the checks (`atask update <group> --check ...`)", epicsFailed))
		}
		s.Reasons = append(s.Reasons, epicErrors...)
		if exhausted > 0 {
			s.Reasons = append(s.Reasons, fmt.Sprintf("%d task(s) exhausted their attempts; fix and `atask update <task> --reset-attempts`", exhausted))
		}
		if workspaceBlocked > 0 {
			s.Reasons = append(s.Reasons, fmt.Sprintf("%d task(s) have an unusable workspace; fix the worktree and `atask claim <task>`, or `atask update <task> --reset-attempts`", workspaceBlocked))
		}
		if drafts > 0 {
			s.Reasons = append(s.Reasons, fmt.Sprintf("%d draft task(s) need both proofs; `atask update <task> --proof ... --regression ...`", drafts))
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
