package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

func runtimeOwner(session, digest string) *workspace.RuntimeIdentity {
	return &workspace.RuntimeIdentity{Session: session, PID: 1, Incarnation: strings.Repeat("a", 64), CapabilityDigest: strings.Repeat(digest, 64)}
}

func runtimeEngine(t *testing.T, base *app.Engine, cwd string, owner *workspace.RuntimeIdentity, now func() time.Time) *app.Engine {
	t.Helper()
	e, err := app.Open(context.Background(), app.Config{Path: base.Path(), Cwd: cwd, Runtime: owner, Now: now, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestRuntimeClaimsDoNotLeakAcrossRuntimes(t *testing.T) {
	ctx := context.Background()
	base, _, cwd, ids := rootlessEngine(t)
	owner := runtimeOwner("native-session", "b")
	own := runtimeEngine(t, base, cwd, owner, nil)
	claim, err := own.Claim(ctx, app.ClaimRequest{TaskID: &ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	for name, identity := range map[string]*workspace.RuntimeIdentity{"no runtime": nil, "different capability": runtimeOwner(owner.Session, "c"), "different session": runtimeOwner("other-session", "b")} {
		t.Run(name, func(t *testing.T) {
			other := runtimeEngine(t, base, cwd, identity, nil)
			tok, err := other.AmbientToken(ctx)
			if err != nil || tok != "" {
				t.Fatalf("foreign discovery: token present=%v error=%v", tok != "", err)
			}
			if _, err := other.Claim(ctx, app.ClaimRequest{TaskID: &ids[0]}); err == nil {
				t.Fatal("foreign runtime adopted claim")
			}
			if _, err := other.Renew(ctx, claim.Token); err != nil {
				t.Fatalf("explicit token lost authority: %v", err)
			}
		})
	}
	tok, err := own.AmbientToken(ctx)
	if err != nil || tok != claim.Token {
		t.Fatalf("owner discovery: %v", err)
	}
}

func TestConcurrentRuntimesDiscoverTheirOwnClaims(t *testing.T) {
	ctx := context.Background()
	base, _, cwd, ids := rootlessEngine(t)
	first := runtimeEngine(t, base, cwd, runtimeOwner("first", "b"), nil)
	second := runtimeEngine(t, base, cwd, runtimeOwner("second", "c"), nil)
	for i, e := range []*app.Engine{first, second} {
		if _, err := e.Claim(ctx, app.ClaimRequest{TaskID: &ids[i]}); err != nil {
			t.Fatal(err)
		}
	}
	for i, e := range []*app.Engine{first, second} {
		tok, err := e.AmbientToken(ctx)
		if err != nil || tok == "" {
			t.Fatalf("own discovery: %v", err)
		}
		view, err := e.Whoami(ctx, tok)
		if err != nil || view.ID != ids[i] {
			t.Fatalf("wrong runtime claim: %v", err)
		}
	}
}

func TestRuntimeRenewAllNeverRevivesExpiredClaims(t *testing.T) {
	ctx := context.Background()
	base, _, cwd, ids := rootlessEngine(t)
	now := time.Now().UTC()
	owner := runtimeEngine(t, base, cwd, runtimeOwner("owner", "b"), func() time.Time { return now })
	var tokens []execution.Token
	for _, id := range ids {
		claim, err := owner.Claim(ctx, app.ClaimRequest{TaskID: new(id)})
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, claim.Token)
	}
	if _, err := owner.AmbientToken(ctx); fault.CodeOf(err) != fault.CodeInvalidInput {
		t.Fatalf("same runtime multiple claims must be ambiguous: %v", err)
	}
	for range 4 {
		now = now.Add(45 * time.Second)
		count, err := owner.RenewRuntime(ctx)
		if err != nil || count != 2 {
			t.Fatalf("renew all count=%d error=%v", count, err)
		}
	}
	now = now.Add(2 * time.Minute)
	count, err := owner.RenewRuntime(ctx)
	if err != nil || count != 0 {
		t.Fatalf("expired count=%d error=%v", count, err)
	}
	for _, tok := range tokens {
		if _, err := owner.Renew(ctx, tok); err == nil {
			t.Fatal("expired token revived")
		}
	}
}
