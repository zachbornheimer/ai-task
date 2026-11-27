package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/verification"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

func TestRuntimeRenewNeverRevivesReleasedOrCompletedClaims(t *testing.T) {
	for _, terminal := range []string{"released", "completed"} {
		t.Run(terminal, func(t *testing.T) {
			ctx := context.Background()
			base, _, cwd, ids := rootlessEngine(t)
			now := time.Now().UTC()
			identity := runtimeOwner("terminal-owner", "b")
			owner := runtimeEngine(t, base, cwd, identity, func() time.Time { return now })
			claim, err := owner.Claim(ctx, app.ClaimRequest{TaskID: &ids[0]})
			if err != nil {
				t.Fatal(err)
			}
			if terminal == "released" {
				err = owner.Release(ctx, app.ReleaseRequest{Token: claim.Token})
			} else {
				result, verifyErr := owner.Verify(ctx, claim.Token, verification.ModeComplete)
				err = verifyErr
				if err == nil && !result.Passed {
					t.Fatal("completion checks did not pass")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			// Restore a stale file to exercise mutation fencing, not merely
			// the successful cleanup performed by release or completion.
			store := workspace.SessionStore{Dir: app.DefaultSessionRoot(base.Path())}
			if err := store.Save(workspace.StoredSession{Cwd: cwd, TaskID: string(ids[0]), Token: string(claim.Token), Runtime: identity}); err != nil {
				t.Fatal(err)
			}
			now = now.Add(30 * time.Second)
			if count, err := owner.RenewRuntime(ctx); err != nil || count != 0 {
				t.Fatalf("terminal renewal: count=%d error=%v", count, err)
			}
			if _, err := owner.Renew(ctx, claim.Token); fault.CodeOf(err) != fault.CodeInvalidSession {
				t.Fatalf("terminal token regained authority: %v", err)
			}
		})
	}
}

func TestRuntimeRenewCannotAffectSupersedingOwner(t *testing.T) {
	ctx := context.Background()
	base, _, cwd, ids := rootlessEngine(t)
	now := time.Now().UTC()
	oldIdentity := runtimeOwner("old-owner", "b")
	oldOwner := runtimeEngine(t, base, cwd, oldIdentity, func() time.Time { return now })
	oldClaim, err := oldOwner.Claim(ctx, app.ClaimRequest{TaskID: &ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	newOwner := runtimeEngine(t, base, cwd, runtimeOwner("new-owner", "c"), func() time.Time { return now })
	newClaim, err := newOwner.Claim(ctx, app.ClaimRequest{TaskID: &ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	if newClaim.AttemptSeq <= oldClaim.AttemptSeq {
		t.Fatal("takeover did not supersede the old attempt")
	}
	// A leftover copy from another directory must not renew the replacement
	// attempt, nor delete its independently owned token during cleanup.
	staleCwd := t.TempDir()
	store := workspace.SessionStore{Dir: app.DefaultSessionRoot(base.Path())}
	if err := store.Save(workspace.StoredSession{Cwd: staleCwd, TaskID: string(ids[0]), Token: string(oldClaim.Token), Runtime: oldIdentity}); err != nil {
		t.Fatal(err)
	}
	if count, err := oldOwner.RenewRuntime(ctx); err != nil || count != 0 {
		t.Fatalf("superseded renewal: count=%d error=%v", count, err)
	}
	if _, err := oldOwner.Renew(ctx, oldClaim.Token); fault.CodeOf(err) != fault.CodeInvalidSession {
		t.Fatalf("superseded token regained authority: %v", err)
	}
	if token, err := newOwner.AmbientToken(ctx); err != nil || token != newClaim.Token {
		t.Fatalf("superseding owner lost authority: %v", err)
	}
	if count, err := newOwner.RenewRuntime(ctx); err != nil || count != 1 {
		t.Fatalf("superseding renewal: count=%d error=%v", count, err)
	}
}
