package workspace

import (
	"context"
	"fmt"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// Requirement is a completed prerequisite: the task and the revision its
// completion recorded.
type Requirement struct {
	Task     string
	Revision string
}

// BaseConflict reports that two completed revisions cannot be combined
// into one starting point. Files are the conflicting paths; First and
// Second are the two requirements whose changes collide.
type BaseConflict struct {
	First, Second Requirement
	Files         []string
}

func (c *BaseConflict) Error() string {
	if c.Second.Task == "" {
		return fmt.Sprintf("%s (revision %s) conflicts with the target branch in %s",
			c.First.Task, short(c.First.Revision), strings.Join(c.Files, ", "))
	}
	return fmt.Sprintf("%s (revision %s) and %s (revision %s) conflict in %s",
		c.First.Task, short(c.First.Revision), c.Second.Task, short(c.Second.Revision), strings.Join(c.Files, ", "))
}

// Rule names why the target already holds a requirement.
type Rule string

const (
	// RuleRevisionInTarget: the completed revision is an ancestor of the target.
	RuleRevisionInTarget Rule = "revision_in_target"
	// RuleBranchTipInTarget: the task branch tip is an ancestor of the target
	// (the branch was rebased before it landed).
	RuleBranchTipInTarget Rule = "branch_tip_in_target"
	// RulePatchApplied: the branch is gone but git cherry reports the
	// completed revision's patch already applied in the target.
	RulePatchApplied Rule = "patch_applied"
)

// Satisfaction records which rule made a requirement add nothing.
type Satisfaction struct {
	Task string
	Rule Rule
}

// BaseResult is a resolved base and the reasoning behind it.
type BaseResult struct {
	Revision  string
	Satisfied []Satisfaction
	Merged    []Requirement // pending requirements, with the revision actually used
	Stale     []Requirement // group only: pending requirements that no longer merge onto the target tip; their proofs are judged on the base instead
}

// Explain lists, one per requirement, how the base treated it.
func (r BaseResult) Explain() []string {
	var out []string
	for _, s := range r.Satisfied {
		out = append(out, fmt.Sprintf("base: %s already in target (%s)", s.Task, s.Rule))
	}
	for _, q := range r.Merged {
		out = append(out, fmt.Sprintf("base: %s merged at %s", q.Task, short(q.Revision)))
	}
	for _, q := range r.Stale {
		out = append(out, fmt.Sprintf("base: %s superseded by the target; proof judged on the target", q.Task))
	}
	return out
}

// Base returns the revision a task branch starts from; see ResolveBase.
func (m Manager) Base(ctx context.Context, target string, reqs []Requirement) (string, error) {
	res, err := m.ResolveBase(ctx, target, reqs)
	return res.Revision, err
}

// ResolveBase computes the starting revision. A requirement the target
// already holds (see Rule) adds nothing. With none left, the base is the
// target tip; otherwise the target tip with every pending requirement
// merged in (a plain fast-forward when the target has not moved), built
// in a scratch worktree that is removed afterwards. Pending requirements
// merge their task branch tip when the branch exists, else the recorded
// revision. A merge that conflicts fails with *BaseConflict wrapped as
// BASE_CONFLICT; one git refuses without conflicted paths is an internal
// error (NOT_READY) carrying git's message. User commit hooks never run.
func (m Manager) ResolveBase(ctx context.Context, target string, reqs []Requirement) (BaseResult, error) {
	return m.resolveBase(ctx, target, reqs, false)
}

// ResolveGroupBase is ResolveBase for group verification. A pending
// requirement that cannot merge onto the target tip alone has been
// integrated and since changed there: it is reported in Stale instead of
// failing, so the caller judges its proof on the target. Pending
// requirements that merge alone but conflict with each other still fail
// with BASE_CONFLICT.
func (m Manager) ResolveGroupBase(ctx context.Context, target string, reqs []Requirement) (BaseResult, error) {
	return m.resolveBase(ctx, target, reqs, true)
}

func (m Manager) resolveBase(ctx context.Context, target string, reqs []Requirement, group bool) (BaseResult, error) {
	tip, err := m.Revision(ctx, "refs/heads/"+target)
	if err != nil {
		return BaseResult{}, err
	}
	res := BaseResult{Revision: tip}
	pending, err := m.classify(ctx, tip, reqs, &res)
	if err != nil {
		return BaseResult{}, err
	}
	if group {
		pending, res.Stale = m.splitStale(ctx, tip, pending)
	}
	res.Merged = pending
	if len(pending) == 0 {
		return res, nil
	}
	if _, err := Identity(ctx, m.Repo); err != nil {
		return BaseResult{}, err
	}
	path, cleanup, err := m.Snapshot(ctx, tip)
	if err != nil {
		return BaseResult{}, err
	}
	defer cleanup()
	for i, req := range pending {
		message := fmt.Sprintf("chore: base of %s on %s", target, req.Task)
		conflicts, err := scratchMerge(ctx, path, message, req.Revision)
		if err != nil {
			if strings.TrimSpace(conflicts) == "" {
				return BaseResult{}, err
			}
			bc := &BaseConflict{First: req, Files: lines(conflicts)}
			if i > 0 {
				bc.First, bc.Second = m.earlierTouching(ctx, tip, pending[:i], conflicts), req
			}
			return BaseResult{}, fault.Wrap(bc, fault.CodeBaseConflict, "%s", bc.Error())
		}
	}
	rev, err := git(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return BaseResult{}, fault.Wrap(err, fault.CodeNotReady, "read base revision")
	}
	res.Revision = strings.TrimSpace(rev)
	return res, nil
}

// splitStale separates the requirements that merge cleanly onto the tip
// alone from those that conflict with it.
func (m Manager) splitStale(ctx context.Context, tip string, pending []Requirement) (clean, stale []Requirement) {
	for _, r := range pending {
		if _, err := git(ctx, m.Repo, "merge-tree", "--write-tree", tip, r.Revision); err != nil {
			stale = append(stale, r)
			continue
		}
		clean = append(clean, r)
	}
	return clean, stale
}

// classify drops requirements without a revision and duplicates, records
// the rule that satisfied each one the target already holds, and returns
// the rest with the revision to merge (branch tip when the branch exists).
func (m Manager) classify(ctx context.Context, tip string, reqs []Requirement, res *BaseResult) ([]Requirement, error) {
	seen := map[string]bool{}
	var out []Requirement
	for _, r := range reqs {
		if r.Revision == "" || seen[r.Revision] {
			continue
		}
		seen[r.Revision] = true
		branchTip, hasBranch := m.branchTip(ctx, r.Task)
		rule, err := m.satisfiedBy(ctx, tip, r, branchTip, hasBranch)
		if err != nil {
			return nil, err
		}
		if rule != "" {
			res.Satisfied = append(res.Satisfied, Satisfaction{Task: r.Task, Rule: rule})
			continue
		}
		if hasBranch {
			r.Revision = branchTip
		}
		out = append(out, r)
	}
	return out, nil
}

func (m Manager) branchTip(ctx context.Context, task string) (string, bool) {
	rev, err := m.Revision(ctx, "refs/heads/"+TaskBranch(task))
	return rev, err == nil
}

func (m Manager) satisfiedBy(ctx context.Context, tip string, r Requirement, branchTip string, hasBranch bool) (Rule, error) {
	in, err := m.containsRev(ctx, tip, r.Revision)
	if err != nil {
		return "", err
	}
	if in {
		return RuleRevisionInTarget, nil
	}
	if hasBranch {
		if in, err = m.containsRev(ctx, tip, branchTip); err != nil {
			return "", err
		}
		if in {
			return RuleBranchTipInTarget, nil
		}
		return "", nil
	}
	if m.patchApplied(ctx, tip, r.Revision) {
		return RulePatchApplied, nil
	}
	return "", nil
}

// patchApplied reports whether git cherry finds the revision's own commit
// already applied in the target by patch-id.
func (m Manager) patchApplied(ctx context.Context, tip, revision string) bool {
	out, err := git(ctx, m.Repo, "cherry", tip, revision, revision+"^")
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(out), "- ")
}

func (m Manager) containsRev(ctx context.Context, tip, revision string) (bool, error) {
	_, err := git(ctx, m.Repo, "merge-base", "--is-ancestor", revision, tip)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "exit status 1") || err.Error() == "" {
		return false, nil
	}
	return false, fault.Wrap(err, fault.CodeNotReady, "compare %s with %s", short(revision), short(tip))
}

// earlierTouching picks, from the requirements merged before the one that
// conflicted, the one that changed a conflicting file; the latest merged
// when none can be told apart.
func (m Manager) earlierTouching(ctx context.Context, tip string, merged []Requirement, conflicts string) Requirement {
	files := map[string]bool{}
	for _, f := range lines(conflicts) {
		files[f] = true
	}
	for _, r := range merged {
		changed, err := git(ctx, m.Repo, "diff", "--name-only", tip, r.Revision)
		if err != nil {
			continue
		}
		for _, f := range lines(changed) {
			if files[f] {
				return r
			}
		}
	}
	if len(merged) == 0 {
		return Requirement{}
	}
	return merged[len(merged)-1]
}

func lines(s string) []string {
	var out []string
	for l := range strings.SplitSeq(strings.TrimSpace(s), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
