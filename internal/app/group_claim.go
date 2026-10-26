package app

import (
	"context"
	"fmt"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// errNoClaimable is claimOnce's answer when no candidate exists. It is
// returned as is so a group claim can tell it from other NOT_READY causes
// by identity and say what the group is waiting for.
var errNoClaimable = fault.New(fault.CodeNotReady, "no claimable task")

// GroupNotReady is the detail of a NOT_READY group claim: nothing is
// claimable now, yet the group is neither done nor stalled.
type GroupNotReady struct {
	Group task.ID `json:"group"`
	// GroupProofsPending: every member of a group in the subtree is complete
	// and its own proofs wait for a verifier; run `atask verify complete` on
	// that group, or claim with --wait.
	GroupProofsPending bool `json:"group_proofs_pending"`
	// Active counts work in flight beneath the group: live claims and
	// running or waiting group verification.
	Active int `json:"active"`
}

// groupHome validates that id names a live group and returns its project.
func (e *Engine) groupHome(ctx context.Context, id task.ID) (project.ID, error) {
	var pid project.ID
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		r, err := tx.GetRecord(id)
		if err != nil {
			return err
		}
		if r.Task.Kind != task.KindGroup {
			return fault.New(fault.CodeInvalidInput, "%s is not a group; claim a task by its reference, or a project with --project", id)
		}
		if r.Task.Archived() {
			return fault.New(fault.CodeInvalidInput, "group %s is archived", id)
		}
		pid = r.Task.ProjectID
		return nil
	})
	return pid, err
}

// groupSummary summarizes a group's subtree: the same counts and stall
// reasons as a project Summary, over the group's executable descendants and
// the proofs of the group and its nested groups. The second result reports
// whether a group's proofs are ready and waiting for a verifier.
func (e *Engine) groupSummary(ctx context.Context, id task.ID) (Summary, bool, error) {
	if err := e.reconcile(ctx); err != nil {
		return Summary{}, false, err
	}
	// Group proofs compare against the target head of their own project,
	// resolved before the read opens: Git never runs inside a transaction.
	var homes []project.ID
	if err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		root, err := tx.GetRecord(id)
		if err != nil {
			return err
		}
		below, err := tx.Descendants(id)
		if err != nil {
			return err
		}
		seen := map[project.ID]bool{}
		for _, r := range append([]*sqlite.Record{&root}, pointers(below)...) {
			if r.Task.Kind == task.KindGroup && !seen[r.Task.ProjectID] {
				seen[r.Task.ProjectID] = true
				homes = append(homes, r.Task.ProjectID)
			}
		}
		return nil
	}); err != nil {
		return Summary{}, false, err
	}
	heads := map[project.ID]targetHead{}
	for _, h := range homes {
		heads[h] = e.resolveTargetHead(ctx, h)
	}
	var s Summary
	var scope *groupScope
	err := e.store.Read(ctx, func(tx *sqlite.Tx) error {
		root, err := tx.GetRecord(id)
		if err != nil {
			return err
		}
		proj, err := tx.GetProject(root.Task.ProjectID)
		if err != nil {
			return err
		}
		below, err := tx.Descendants(id)
		if err != nil {
			return err
		}
		scope = &groupScope{id: id, inside: map[task.ID]bool{id: true}}
		for i := range below {
			scope.inside[below[i].Task.ID] = true
		}
		s, err = e.summarize(tx, proj, append([]sqlite.Record{root}, below...), heads, nil, scope)
		return err
	})
	if err != nil {
		return Summary{}, false, err
	}
	return s, scope.proofsPending, nil
}

// groupIdle answers a group claim that found nothing to claim: DONE,
// STALLED with the reasons, or NOT_READY saying whether a verifier is due.
func (e *Engine) groupIdle(ctx context.Context, id task.ID) error {
	sum, pending, err := e.groupSummary(ctx, id)
	if err != nil {
		return err
	}
	switch {
	case sum.Done:
		return ErrDone
	case sum.Stalled:
		return &fault.Error{Code: fault.CodeStalled, Message: ErrStalled.Message, Details: sum}
	}
	msg := fmt.Sprintf("group %s has nothing claimable right now (%d in flight)", id, sum.Active)
	if pending {
		msg += "; every member of a group beneath it is complete and its proofs await a verifier: `atask claim --group " + string(id) + " --wait` runs them, or `atask verify complete` on that group"
	}
	return &fault.Error{
		Code:    fault.CodeNotReady,
		Message: msg,
		Details: GroupNotReady{Group: id, GroupProofsPending: pending, Active: sum.Active},
	}
}

// outsideRequirements lists the incomplete requirements of a task that lie
// outside the group scope; none without a scope.
func outsideRequirements(tx *sqlite.Tx, r sqlite.Record, scope *groupScope) ([]sqlite.Record, error) {
	if scope == nil {
		return nil, nil
	}
	reqs, err := tx.Requirements(r.Task.ID)
	if err != nil {
		return nil, err
	}
	var out []sqlite.Record
	for i := range reqs {
		if reqs[i].CompletedAt == nil && !scope.inside[reqs[i].Task.ID] {
			out = append(out, reqs[i])
		}
	}
	return out, nil
}

func pointers(rs []sqlite.Record) []*sqlite.Record {
	out := make([]*sqlite.Record, len(rs))
	for i := range rs {
		out[i] = &rs[i]
	}
	return out
}
