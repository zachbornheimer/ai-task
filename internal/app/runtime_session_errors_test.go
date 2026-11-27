package app_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

func TestRuntimeRenewFromDifferentDirectoryOwnsAllRuntimeClaims(t *testing.T) {
	ctx := context.Background()
	base, _, cwd, ids := rootlessEngine(t)
	identity := runtimeOwner("owner", "b")
	owner := runtimeEngine(t, base, cwd, identity, nil)
	if _, err := owner.Claim(ctx, app.ClaimRequest{TaskID: &ids[0]}); err != nil {
		t.Fatal(err)
	}
	elsewhere := runtimeEngine(t, base, t.TempDir(), identity, nil)
	if count, err := elsewhere.RenewRuntime(ctx); err != nil || count != 1 {
		t.Fatalf("renew across cwd: count=%d err=%v", count, err)
	}
}

func TestRuntimeRenewDoesNotAdoptLegacyClaims(t *testing.T) {
	ctx := context.Background()
	base, _, cwd, ids := rootlessEngine(t)
	if _, err := base.Claim(ctx, app.ClaimRequest{TaskID: &ids[0]}); err != nil {
		t.Fatal(err)
	}
	owner := runtimeEngine(t, base, cwd, runtimeOwner("owner", "b"), nil)
	if count, err := owner.RenewRuntime(ctx); err != nil || count != 0 {
		t.Fatalf("legacy adoption: count=%d err=%v", count, err)
	}
	if tok, err := owner.AmbientToken(ctx); err != nil || tok != "" {
		t.Fatalf("runtime selected a legacy cwd claim: %v", err)
	}
}

func TestRuntimeRenewReportsStorageAndDatabaseFailures(t *testing.T) {
	ctx := context.Background()
	base, state, cwd, ids := rootlessEngine(t)
	owner := runtimeEngine(t, base, cwd, runtimeOwner("owner", "b"), nil)
	_, err := owner.Claim(ctx, app.ClaimRequest{TaskID: &ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	// Closing only this engine leaves a valid token file: scan must not hide
	// database failure by claiming the runtime has no live work.
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.RenewRuntime(ctx); err == nil {
		t.Fatal("closed database was treated as no claims")
	}
	// An unreadable scan entry must also stop the renewal operation.
	if err := os.Mkdir(filepath.Join(state, "sessions", "unreadable-entry"), 0o700); err != nil {
		t.Fatal(err)
	}
	scan := workspace.SessionStore{Dir: filepath.Join(state, "sessions")}
	if _, err := scan.AllChecked(); err == nil {
		t.Fatal("unreadable state ignored")
	}
}
