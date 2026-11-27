package workspace

import (
	"os"
	"strings"
	"testing"
)

func TestSessionStorePreservesLegacyAndRuntimeBindings(t *testing.T) {
	store := SessionStore{Dir: t.TempDir()}
	binding := &RuntimeIdentity{Session: "native-session", PID: 123, Incarnation: strings.Repeat("a", 64), CapabilityDigest: strings.Repeat("b", 64)}
	for _, owner := range []*RuntimeIdentity{nil, binding} {
		value := StoredSession{Cwd: "/claim", TaskID: "task", Token: "token", Runtime: owner}
		if err := store.Save(value); err != nil {
			t.Fatal(err)
		}
		loaded, ok := store.Find(value.Cwd, value.TaskID)
		if !ok || loaded.Token != value.Token || (owner == nil) != (loaded.Runtime == nil) {
			t.Fatalf("binding lost: %+v", loaded)
		}
		if owner != nil && *loaded.Runtime != *owner {
			t.Fatal("binding changed")
		}
	}
	// Invalid extended records must never become legacy bearer-token files.
	if err := os.WriteFile(store.path("/claim", "task"), []byte("/claim\ntask\ntoken\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Find("/claim", "task"); ok {
		t.Fatal("invalid binding downgraded to unbound")
	}
}

func TestRuntimeIdentityRequiresEveryBindingComponent(t *testing.T) {
	owner := &RuntimeIdentity{Session: "session", PID: 1, Incarnation: strings.Repeat("a", 64), CapabilityDigest: strings.Repeat("b", 64)}
	if !owner.Allows(owner) {
		t.Fatal("owner rejected")
	}
	var absent *RuntimeIdentity
	if absent.Allows(owner) {
		t.Fatal("absent runtime accepted")
	}
	for _, change := range []func(*RuntimeIdentity){func(r *RuntimeIdentity) { r.Session = "other" }, func(r *RuntimeIdentity) { r.PID++ }, func(r *RuntimeIdentity) { r.Incarnation = strings.Repeat("c", 64) }, func(r *RuntimeIdentity) { r.CapabilityDigest = strings.Repeat("c", 64) }} {
		different := *owner
		change(&different)
		if different.Allows(owner) {
			t.Fatal("different binding accepted")
		}
	}
	if !absent.Allows(nil) {
		t.Fatal("legacy unbound claim rejected")
	}
}
