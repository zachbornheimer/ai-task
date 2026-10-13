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
	return fmt.Sprintf("%s (revision %s) and %s (revision %s) conflict in %s",
		c.First.Task, short(c.First.Revision), c.Second.Task, short(c.Second.Revision), strings.Join(c.Files, ", "))
}

// Base returns the revision a task branch starts from. Requirements whose
// revision the target already contains add nothing. With none left, the
// base is the target tip; with one, that requirement's revision (a
// stacked branch); with several, a merge commit of all of them onto the
// target tip, built in a scratch worktree that is removed afterwards. A
// merge that conflicts fails with *BaseConflict wrapped as BASE_CONFLICT.
func (m Manager) Base(ctx context.Context, target string, reqs []Requirement) (string, error) {
	tip, err := m.Revision(ctx, "refs/heads/"+target)
	if err != nil {
		return "", err
	}
	pending, err := m.notIn(ctx, tip, reqs)
	if err != nil {
		return "", err
	}
	switch len(pending) {
	case 0:
		return tip, nil
	case 1:
		return pending[0].Revision, nil
	}
	if _, err := Identity(ctx, m.Repo); err != nil {
		return "", err
	}
	path, cleanup, err := m.Snapshot(ctx, tip)
	if err != nil {
		return "", err
	}
	defer cleanup()
	for i, req := range pending {
		message := fmt.Sprintf("chore: base of %s on %s", target, req.Task)
		if _, err := git(ctx, path, "merge", "--no-ff", "--no-edit", "-m", message, req.Revision); err != nil {
			conflicts, _ := git(ctx, path, "diff", "--name-only", "--diff-filter=U")
			_, _ = git(ctx, path, "merge", "--abort")
			bc := &BaseConflict{Second: req, First: m.earlierTouching(ctx, tip, pending[:i], conflicts), Files: lines(conflicts)}
			return "", fault.Wrap(bc, fault.CodeBaseConflict, "%s", bc.Error())
		}
	}
	rev, err := git(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return "", fault.Wrap(err, fault.CodeNotReady, "read base revision")
	}
	return strings.TrimSpace(rev), nil
}

// notIn drops requirements without a revision, duplicates, and those whose
// revision is already an ancestor of tip.
func (m Manager) notIn(ctx context.Context, tip string, reqs []Requirement) ([]Requirement, error) {
	seen := map[string]bool{}
	var out []Requirement
	for _, r := range reqs {
		if r.Revision == "" || seen[r.Revision] {
			continue
		}
		seen[r.Revision] = true
		in, err := m.containsRev(ctx, tip, r.Revision)
		if err != nil {
			return nil, err
		}
		if !in {
			out = append(out, r)
		}
	}
	return out, nil
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
