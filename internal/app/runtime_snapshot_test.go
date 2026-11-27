package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
)

func TestRuntimeStaleSnapshotCannotDeleteSuccessor(t *testing.T) {
	for _, operation := range []string{"renew", "discover"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			base, _, cwd, ids := rootlessEngine(t)
			now := time.Now().UTC()
			var interleave func()
			old := runtimeEngine(t, base, cwd, runtimeOwner("old", "b"), func() time.Time {
				if interleave != nil {
					action := interleave
					interleave = nil
					action()
				}
				return now
			})
			if _, err := old.Claim(ctx, app.ClaimRequest{TaskID: &ids[0]}); err != nil {
				t.Fatal(err)
			}
			now = now.Add(2 * time.Minute)
			successor := runtimeEngine(t, base, cwd, runtimeOwner("new", "c"), func() time.Time { return now })
			var replacement app.Session
			interleave = func() {
				var err error
				replacement, err = successor.Claim(ctx, app.ClaimRequest{TaskID: &ids[0]})
				if err != nil {
					t.Fatal(err)
				}
			}
			if operation == "renew" {
				if count, err := old.RenewRuntime(ctx); err != nil || count != 0 {
					t.Fatalf("stale renewal count=%d err=%v", count, err)
				}
			} else {
				if token, err := old.AmbientToken(ctx); err != nil || token != "" {
					t.Fatalf("stale discovery retained authority: %v", err)
				}
			}
			if replacement.Token == "" {
				t.Fatal("interleaving did not execute")
			}
			if token, err := successor.AmbientToken(ctx); err != nil || token != replacement.Token {
				t.Fatalf("stale snapshot deleted successor: token present=%v err=%v", token != "", err)
			}
		})
	}
}
