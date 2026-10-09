package app

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/dependency"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// Apply applies a ChangeSet as one atomic plan revision. Either every
// operation is valid against the plan as it stands after the previous
// operations, or nothing changes. Validation covers unknown references,
// cyclic hard edges, cross-project links, duplicate keys with conflicting
// definitions, illegal archives, and edits to claimed or completed
// contracts. A replay with the same IdempotencyKey returns the stored
// result; a stale ExpectedPlanRev is rejected with PLAN_CONFLICT.
func (e *Engine) Apply(ctx context.Context, cs plan.ChangeSet) (plan.Result, error) {
	if err := cs.Validate(); err != nil {
		return plan.Result{}, err
	}
	now := e.now()
	var result plan.Result
	err := e.store.Write(ctx, func(tx *sqlite.Tx) error {
		proj, err := tx.GetProject(cs.ProjectID)
		if err != nil {
			return err
		}
		if cs.IdempotencyKey != "" {
			if body, ok, err := tx.PlanChange(cs.ProjectID, cs.IdempotencyKey); err != nil {
				return err
			} else if ok {
				if err := json.Unmarshal([]byte(body), &result); err != nil {
					return fault.Wrap(err, fault.CodeInternal, "decode stored plan result")
				}
				result.Replayed = true
				return nil
			}
		}
		rev, err := tx.BumpPlanRev(cs.ProjectID, cs.ExpectedPlanRev, now)
		if err != nil {
			return err
		}
		a := &applier{e: e, tx: tx, cs: cs, proj: proj, now: now, refs: map[string]task.ID{}, result: plan.Result{PlanRev: rev, Created: map[string]task.ID{}}}
		for i, op := range cs.Operations {
			if err := a.apply(ctx, i, op); err != nil {
				return err
			}
		}
		if err := a.checkArchives(); err != nil {
			return err
		}
		result = a.result
		if cs.IdempotencyKey != "" {
			body, _ := json.Marshal(result)
			if err := tx.RecordPlanChange(cs.ProjectID, cs.IdempotencyKey, rev, string(body), now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return plan.Result{}, err
	}
	e.notify()
	return result, nil
}

// applier carries the state of one Apply transaction.
type applier struct {
	e        *Engine
	tx       *sqlite.Tx
	cs       plan.ChangeSet
	proj     project.Project
	now      time.Time
	refs     map[string]task.ID // keys created in this set
	archived []task.ID
	result   plan.Result
}

// resolve turns a Ref into an existing task record.
func (a *applier) resolve(r plan.Ref) (sqlite.Record, error) {
	if r.IsID() {
		rec, err := a.tx.GetRecord(task.ID(r))
		if err != nil {
			return rec, err
		}
		if rec.Task.ProjectID != a.cs.ProjectID {
			return rec, fault.New(fault.CodeCrossProject, "%s belongs to another project", r)
		}
		return rec, nil
	}
	if id, ok := a.refs[string(r)]; ok {
		return a.tx.GetRecord(id)
	}
	rec, ok, err := a.tx.GetByKey(a.cs.ProjectID, string(r))
	if err != nil {
		return rec, err
	}
	if !ok {
		return rec, fault.New(fault.CodeNotFound, "no task with key or id %q", r)
	}
	return rec, nil
}

func (a *applier) apply(ctx context.Context, i int, op plan.Change) error {
	switch c := op.(type) {
	case plan.AddGroup:
		return a.addGroup(i, c)
	case plan.AddTask:
		return a.addTask(ctx, i, c)
	case plan.UpdateTask:
		return a.updateTask(ctx, c)
	case plan.ArchiveTask:
		return a.archive(c)
	}
	return fault.New(fault.CodeInvalidInput, "unknown change")
}

func (a *applier) createdName(i int, key string) string {
	if key != "" {
		return key
	}
	return fmt.Sprintf("#%d", i)
}

func (a *applier) parentGroup(ref plan.Ref) (task.ID, error) {
	if ref == "" {
		return "", nil
	}
	g, err := a.resolve(ref)
	if err != nil {
		return "", err
	}
	if g.Task.Kind != task.KindGroup || g.Task.Archived() {
		return "", fault.New(fault.CodeInvalidInput, "%s is not a live group", ref)
	}
	return g.Task.ID, nil
}

func (a *applier) addGroup(i int, c plan.AddGroup) error {
	parent, err := a.parentGroup(c.Parent)
	if err != nil {
		return err
	}
	if c.Key != "" {
		if existing, ok, err := a.tx.GetByKey(a.cs.ProjectID, c.Key); err != nil {
			return err
		} else if ok {
			if existing.Task.Kind == task.KindGroup && !existing.Task.Archived() && existing.Task.Description == c.Title && existing.Task.ParentID == parent {
				a.refs[c.Key] = existing.Task.ID
				return nil // identical replay
			}
			return fault.New(fault.CodeDuplicateKey, "key %q already names %s with a different definition", c.Key, existing.Task.ID)
		}
	}
	spec := task.Spec{ProjectID: a.cs.ProjectID, Kind: task.KindGroup, Key: c.Key, ParentID: parent, Description: c.Title}
	id, err := a.insert(spec)
	if err != nil {
		return err
	}
	a.result.Created[a.createdName(i, c.Key)] = id
	a.result.Changed++
	return nil
}

func (a *applier) insert(spec task.Spec) (task.ID, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if cid := verification.Collides(spec.Verification.TaskChecks, a.proj.Regression); cid != "" {
		return "", fault.New(fault.CodeInvalidInput, "task check id %q collides with a project regression check; task checks cannot shadow regression checks", cid)
	}
	t := task.Task{ProjectID: spec.ProjectID, Kind: spec.Kind, Key: spec.Key, ParentID: spec.ParentID, Description: spec.Description, Outcome: spec.Outcome, Constraints: spec.Constraints, Acceptance: spec.Acceptance, Verification: spec.Verification, Cohort: spec.Cohort, CreatedAt: a.now, UpdatedAt: a.now}
	for attempt := 0; attempt < 5; attempt++ {
		t.ID = a.e.newID()
		err := a.tx.InsertTask(t)
		if err == nil {
			if spec.Key != "" {
				a.refs[spec.Key] = t.ID
			}
			return t.ID, nil
		}
		if !sqlite.IsUniqueViolation(err) {
			return "", err
		}
		// A key collision cannot be an ID collision retry; report it.
		if spec.Key != "" {
			if _, ok, _ := a.tx.GetByKey(a.cs.ProjectID, spec.Key); ok {
				return "", fault.New(fault.CodeDuplicateKey, "key %q already exists", spec.Key)
			}
		}
	}
	return "", fault.New(fault.CodeInternal, "could not allocate a unique task id")
}

func (a *applier) addTask(ctx context.Context, i int, c plan.AddTask) error {
	parent, err := a.parentGroup(c.Parent)
	if err != nil {
		return err
	}
	spec := task.Spec{ProjectID: a.cs.ProjectID, Kind: task.KindTask, Key: c.Key, ParentID: parent, Description: c.Title, Outcome: c.Outcome, Constraints: c.Constraints, Cohort: c.Cohort, Verification: verification.Policy{TaskChecks: c.TaskChecks}}
	for _, acc := range c.Acceptance {
		spec.Acceptance = append(spec.Acceptance, task.AcceptanceCriterion{Description: acc})
	}
	if err := spec.Validate(); err != nil {
		return err
	}
	// A task is implementable only with its own checks: completion fails
	// closed without them, so planning fails closed too.
	if len(spec.Verification.TaskChecks) == 0 {
		return fault.New(fault.CodeMissingVerification, "task %q needs at least one task check (--check); a task without checks can never complete", c.Title)
	}
	var requires []task.ID
	for _, r := range c.Requires {
		rec, err := a.resolve(r)
		if err != nil {
			return err
		}
		requires = append(requires, rec.Task.ID)
	}
	if c.Key != "" {
		if existing, ok, err := a.tx.GetByKey(a.cs.ProjectID, c.Key); err != nil {
			return err
		} else if ok {
			same, err := a.sameDefinition(existing, spec, requires)
			if err != nil {
				return err
			}
			if same {
				a.refs[c.Key] = existing.Task.ID
				return nil
			}
			return fault.New(fault.CodeDuplicateKey, "key %q already names %s with a different definition; use an update to change it", c.Key, existing.Task.ID)
		}
	}
	id, err := a.insert(spec)
	if err != nil {
		return err
	}
	var edges []dependency.Edge
	for _, r := range requires {
		edges = append(edges, dependency.Edge{Task: id, Requires: r})
	}
	for _, b := range c.Blocks {
		rec, err := a.resolve(b)
		if err != nil {
			return err
		}
		if rec.CompletedAt != nil {
			return fault.New(fault.CodePlanConflict, "%s is complete; a new prerequisite cannot be added to a completed task", rec.Task.ID)
		}
		if err := a.authorizeEligibilityChange(rec); err != nil {
			return err
		}
		edges = append(edges, dependency.Edge{Task: rec.Task.ID, Requires: id})
	}
	if err := a.insertEdges(ctx, edges); err != nil {
		return err
	}
	a.result.Created[a.createdName(i, c.Key)] = id
	a.result.Changed++
	if w := spec.Warnings(); len(w) > 0 {
		if a.result.Warnings == nil {
			a.result.Warnings = map[string][]string{}
		}
		a.result.Warnings[a.createdName(i, c.Key)] = w
	}
	return nil
}

// sameDefinition reports whether an existing task matches a proposed
// definition exactly (title, outcome, constraints, acceptance, checks,
// cohort, parent, and prerequisite set).
func (a *applier) sameDefinition(existing sqlite.Record, spec task.Spec, requires []task.ID) (bool, error) {
	t := existing.Task
	if t.Archived() || t.Kind != spec.Kind || t.Description != spec.Description || t.Outcome != spec.Outcome || t.Cohort != spec.Cohort || t.ParentID != spec.ParentID {
		return false, nil
	}
	if err := a.tx.LoadAcceptance(&t); err != nil {
		return false, err
	}
	if !reflect.DeepEqual(nonEmpty(t.Constraints), nonEmpty(spec.Constraints)) || len(t.Acceptance) != len(spec.Acceptance) {
		return false, nil
	}
	for i := range t.Acceptance {
		if t.Acceptance[i] != spec.Acceptance[i] {
			return false, nil
		}
	}
	if t.Verification.Digest() != spec.Verification.Digest() {
		return false, nil
	}
	reqs, err := a.tx.Requirements(t.ID)
	if err != nil {
		return false, err
	}
	have := map[task.ID]bool{}
	for _, r := range reqs {
		have[r.Task.ID] = true
	}
	if len(have) != len(requires) {
		return false, nil
	}
	for _, r := range requires {
		if !have[r] {
			return false, nil
		}
	}
	return true, nil
}

func nonEmpty(s []string) []string {
	if len(s) == 0 {
		return []string{}
	}
	return s
}

// authorizeEligibilityChange guards edits that change a claimed task's
// eligibility: the agent holding it (session token) or the planner.
func (a *applier) authorizeEligibilityChange(target sqlite.Record) error {
	att := target.Attempt
	if att == nil || att.EndedAt != nil || !att.LeaseActive(a.now) {
		return nil
	}
	if a.cs.Planner {
		return nil
	}
	if a.cs.Session != "" {
		tok, err := execution.ParseToken(a.cs.Session)
		if err != nil {
			return err
		}
		auth, err := a.tx.AttemptByDigest(tok.Digest())
		if err != nil {
			return err
		}
		if auth.Attempt.TaskID == target.Task.ID && execution.Authorize(auth.Attempt, auth.CurrentSeq, a.now) == nil {
			return nil
		}
		return fault.New(fault.CodeSessionSuperseded, "session does not hold %s", target.Task.ID)
	}
	return fault.New(fault.CodePlanConflict, "%s is claimed; changing its prerequisites needs its session token (AT_SESSION) or planner authority (--planner)", target.Task.ID)
}

func (a *applier) insertEdges(ctx context.Context, edges []dependency.Edge) error {
	for _, edge := range edges {
		pa, ka, archA, err := a.tx.TaskProject(edge.Task)
		if err != nil {
			return err
		}
		pb, kb, archB, err := a.tx.TaskProject(edge.Requires)
		if err != nil {
			return err
		}
		if ka != task.KindTask || kb != task.KindTask {
			return fault.New(fault.CodeInvalidInput, "dependencies connect executable tasks only; %s or %s is a group", edge.Task, edge.Requires)
		}
		if archA || archB {
			return fault.New(fault.CodePlanConflict, "dependency on an archived task (%s requires %s)", edge.Task, edge.Requires)
		}
		if err := dependency.Validate(ctx, edge, pa == pb, a.tx); err != nil {
			return err
		}
		if err := a.tx.InsertEdge(edge, a.now); err != nil {
			return err
		}
	}
	return nil
}

func (a *applier) updateTask(ctx context.Context, c plan.UpdateTask) error {
	rec, err := a.resolve(c.Target)
	if err != nil {
		return err
	}
	t := rec.Task
	if t.Archived() {
		return fault.New(fault.CodePlanConflict, "%s is archived", t.ID)
	}
	if err := a.tx.LoadAcceptance(&t); err != nil {
		return err
	}
	contract := c.Title != nil || c.Outcome != nil || c.Constraints.Set || c.Acceptance.Set || c.Cohort != nil || c.TaskChecks.Set || c.Parent != nil
	edges := c.Requires.Set || len(c.AddRequires) > 0 || len(c.RemoveRequires) > 0
	if rec.CompletedAt != nil && (contract || edges) {
		return fault.New(fault.CodePlanConflict, "%s is complete; its contract and prerequisites cannot change without archiving it and planning a new task", t.ID)
	}
	claimed := rec.Attempt != nil && rec.Attempt.EndedAt == nil && rec.Attempt.LeaseActive(a.now)
	if claimed && contract && !a.cs.Planner {
		return fault.New(fault.CodePlanConflict, "%s is claimed; its contract can only change with planner authority (--planner), and a running verification will fail closed", t.ID)
	}
	if edges {
		if err := a.authorizeEligibilityChange(rec); err != nil {
			return err
		}
	}
	changed := false
	if c.Title != nil {
		t.Description, changed = *c.Title, true
	}
	if c.Outcome != nil {
		t.Outcome, changed = *c.Outcome, true
	}
	if c.Constraints.Set {
		t.Constraints, changed = c.Constraints.Value, true
	}
	if c.Acceptance.Set {
		t.Acceptance = nil
		for _, s := range c.Acceptance.Value {
			t.Acceptance = append(t.Acceptance, task.AcceptanceCriterion{Description: s})
		}
		changed = true
	}
	if c.Cohort != nil {
		t.Cohort, changed = *c.Cohort, true
	}
	if c.TaskChecks.Set {
		if t.Kind == task.KindTask && len(c.TaskChecks.Value) == 0 {
			return fault.New(fault.CodeMissingVerification, "%s needs at least one task check; archive it instead of removing its checks", t.ID)
		}
		t.Verification = verification.Policy{TaskChecks: c.TaskChecks.Value}
		changed = true
	}
	if c.Parent != nil {
		parent, err := a.parentGroup(*c.Parent)
		if err != nil {
			return err
		}
		if parent != "" {
			if t.Kind == task.KindGroup {
				if cyc, err := a.tx.ParentChainContains(parent, t.ID); err != nil {
					return err
				} else if cyc {
					return fault.New(fault.CodeDependencyCycle, "group %s cannot be nested inside itself", t.ID)
				}
			}
		}
		t.ParentID, changed = parent, true
	}
	if contract {
		spec := task.Spec{ProjectID: t.ProjectID, Kind: t.Kind, Key: t.Key, ParentID: t.ParentID, Description: t.Description, Outcome: t.Outcome, Constraints: t.Constraints, Acceptance: t.Acceptance, Verification: t.Verification, Cohort: t.Cohort}
		if err := spec.Validate(); err != nil {
			return err
		}
		if cid := verification.Collides(spec.Verification.TaskChecks, a.proj.Regression); cid != "" {
			return fault.New(fault.CodeInvalidInput, "task check id %q collides with a project regression check", cid)
		}
		t.Description, t.Outcome, t.Constraints, t.Acceptance, t.Cohort = spec.Description, spec.Outcome, spec.Constraints, spec.Acceptance, spec.Cohort
		if err := a.tx.UpdateTaskContract(t, a.now); err != nil {
			return err
		}
	}
	if edges {
		if t.Kind != task.KindTask {
			return fault.New(fault.CodeInvalidInput, "%s is a group; groups have no prerequisites", t.ID)
		}
		current, err := a.tx.Requirements(t.ID)
		if err != nil {
			return err
		}
		have := map[task.ID]bool{}
		for _, r := range current {
			have[r.Task.ID] = true
		}
		want := map[task.ID]bool{}
		if c.Requires.Set {
			for _, r := range c.Requires.Value {
				rec, err := a.resolve(r)
				if err != nil {
					return err
				}
				want[rec.Task.ID] = true
			}
		} else {
			for id := range have {
				want[id] = true
			}
		}
		for _, r := range c.AddRequires {
			rec, err := a.resolve(r)
			if err != nil {
				return err
			}
			want[rec.Task.ID] = true
		}
		for _, r := range c.RemoveRequires {
			rec, err := a.resolve(r)
			if err != nil {
				return err
			}
			delete(want, rec.Task.ID)
		}
		for id := range have {
			if !want[id] {
				if _, err := a.tx.DeleteEdge(dependency.Edge{Task: t.ID, Requires: id}); err != nil {
					return err
				}
				changed = true
			}
		}
		var add []dependency.Edge
		for id := range want {
			if !have[id] {
				add = append(add, dependency.Edge{Task: t.ID, Requires: id})
			}
		}
		// Deterministic order for stable error messages.
		for i := 1; i < len(add); i++ {
			for j := i; j > 0 && add[j].Requires < add[j-1].Requires; j-- {
				add[j], add[j-1] = add[j-1], add[j]
			}
		}
		if len(add) > 0 {
			if err := a.insertEdges(ctx, add); err != nil {
				return err
			}
			changed = true
		}
	}
	if c.ResetAttempts {
		if err := a.tx.ResetAttempts(t.ID, a.now); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		a.result.Updated = append(a.result.Updated, t.ID)
		a.result.Changed++
	}
	return nil
}

func (a *applier) archive(c plan.ArchiveTask) error {
	rec, err := a.resolve(c.Target)
	if err != nil {
		return err
	}
	t := rec.Task
	if t.Archived() {
		return nil
	}
	if rec.CompletedAt != nil {
		return fault.New(fault.CodePlanConflict, "%s is complete and cannot be archived", t.ID)
	}
	if att := rec.Attempt; att != nil && att.EndedAt == nil && att.LeaseActive(a.now) {
		return fault.New(fault.CodePlanConflict, "%s is claimed; release it before archiving", t.ID)
	}
	if rec.Submission != nil && rec.Submission.RunStatus() == sqlite.RunPending {
		return fault.New(fault.CodePlanConflict, "%s has a submission awaiting verification; it cannot be archived", t.ID)
	}
	if err := a.tx.ArchiveTask(t.ID, strings.TrimSpace(c.Reason), a.now); err != nil {
		return err
	}
	a.archived = append(a.archived, t.ID)
	a.result.Archived = append(a.result.Archived, t.ID)
	a.result.Changed++
	return nil
}

// checkArchives rejects the set if an archived task still has live
// dependents or live children; the same set must repair them.
func (a *applier) checkArchives() error {
	for _, id := range a.archived {
		deps, err := a.tx.Dependents(id)
		if err != nil {
			return err
		}
		for _, d := range deps {
			if !d.Task.Archived() {
				return fault.New(fault.CodePlanConflict, "archiving %s would leave %s with a dangling prerequisite; remove that edge or archive %s in the same change set", id, d.Task.ID, d.Task.ID)
			}
		}
		children, err := a.tx.Children(id)
		if err != nil {
			return err
		}
		if len(children) > 0 {
			return fault.New(fault.CodePlanConflict, "archiving group %s would orphan %d member(s); move or archive them in the same change set", id, len(children))
		}
	}
	return nil
}
