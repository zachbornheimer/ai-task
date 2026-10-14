package app

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/proof"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

const checkActorAttemptPrefix = "attempt:"

// checkSpecs renders the specs of a policy for each recorded field.
func checkSpecs(pol verification.Policy) map[task.CheckField]string {
	var waited, detached []string
	for _, a := range pol.OnComplete {
		spec := fullSpec(a.Proof())
		if a.Detach {
			detached = append(detached, spec)
		} else {
			waited = append(waited, spec)
		}
	}
	return map[task.CheckField]string{
		task.CheckFieldProof:            specString(pol.Proof),
		task.CheckFieldRegression:       specString(pol.RegressionProof),
		task.CheckFieldOnComplete:       strings.Join(waited, "; "),
		task.CheckFieldOnCompleteDetach: strings.Join(detached, "; "),
	}
}

func specString(s *proof.ProofSpec) string {
	if s == nil {
		return ""
	}
	return fullSpec(*s)
}

// fullSpec renders a spec with its options and size, so two specs that
// differ only in an option or the size read differently in the history.
func fullSpec(s proof.ProofSpec) string {
	var out strings.Builder
	out.WriteString(s.String())
	for _, k := range slices.Sorted(maps.Keys(s.Options)) {
		out.WriteString(" " + k + "=" + s.Options[k])
	}
	if s.Size != "" {
		out.WriteString(" size=" + string(s.Size))
	}
	return out.String()
}

var checkFieldOrder = []task.CheckField{task.CheckFieldProof, task.CheckFieldRegression, task.CheckFieldOnComplete, task.CheckFieldOnCompleteDetach}

// recordCheckChanges appends one history row per check field that differs
// between before and after. The caller decides whether the task qualifies.
func (a *applier) recordCheckChanges(id task.ID, before, after verification.Policy) error {
	old, now := checkSpecs(before), checkSpecs(after)
	var actor string
	for _, f := range checkFieldOrder {
		if old[f] == now[f] {
			continue
		}
		if actor == "" {
			var err error
			if actor, err = a.checkActor(); err != nil {
				return err
			}
		}
		change := task.CheckChange{ChangedAt: a.now, Field: f, OldSpec: old[f], NewSpec: now[f], Actor: actor, Cwd: a.e.cwd}
		if err := a.tx.AppendCheckChange(id, change); err != nil {
			return err
		}
	}
	return nil
}

// checkActor names who made the change: the attempt of the session token
// presented, else the planner.
func (a *applier) checkActor() (string, error) {
	if a.cs.Session == "" {
		return task.CheckActorPlanner, nil
	}
	tok, err := execution.ParseToken(a.cs.Session)
	if err != nil {
		return "", err
	}
	auth, err := a.tx.AttemptByDigest(tok.Digest())
	if err != nil {
		return "", err
	}
	return checkActorAttemptPrefix + strconv.FormatInt(auth.Attempt.ID, 10), nil
}

// attachCheckChanges reports the check changes made since the token's
// attempt began.
func (e *Engine) attachCheckChanges(ctx context.Context, token execution.Token, res *VerifyResult) error {
	return e.store.Read(ctx, func(tx *sqlite.Tx) error {
		auth, err := tx.AttemptByDigest(token.Digest())
		if err != nil {
			return err
		}
		changes, err := tx.CheckChanges(auth.Attempt.TaskID, auth.Attempt.StartedAt.Truncate(time.Millisecond))
		if err != nil {
			return err
		}
		res.ChecksChangedAfterClaim = len(changes) > 0
		if res.ChecksChangedAfterClaim {
			res.CheckChanges = changes
		}
		return nil
	})
}
