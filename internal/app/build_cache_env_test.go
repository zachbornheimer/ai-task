package app_test

import "testing"

// buildCacheProbe passes only when the build-cache variables arrived and
// the registry credentials that share the CARGO_ prefix did not.
const buildCacheProbe = `test "$CARGO_TARGET_DIR" = bc-target && test "$CARGO_HOME" = bc-home && test "$RUSTUP_HOME" = bc-rustup && test "$SCCACHE_DIR" = bc-sccache && test -z "$CARGO_REGISTRY_TOKEN" && test -z "$CARGO_REGISTRIES_PRIVATE_TOKEN"`

// TestSnapshotChecksSeeBuildCacheEnv: `verify complete` runs each suite in
// a fresh disposable snapshot, so a build that is warm in the working tree
// is cold there and a heavy suite (cargo) can spend its whole budget
// compiling. The build-cache variables a user points at a shared
// directory must reach those snapshot checks; credentials that share the
// CARGO_ prefix must not.
func TestSnapshotChecksSeeBuildCacheEnv(t *testing.T) {
	clearRemoteStore(t)
	t.Setenv("CARGO_TARGET_DIR", "bc-target")
	t.Setenv("CARGO_HOME", "bc-home")
	t.Setenv("RUSTUP_HOME", "bc-rustup")
	t.Setenv("SCCACHE_DIR", "bc-sccache")
	t.Setenv("CARGO_REGISTRY_TOKEN", "leak-cargo-registry-token")
	t.Setenv("CARGO_REGISTRIES_PRIVATE_TOKEN", "leak-private-registry-token")
	f := newGitFixture(t)
	id := f.addWith("cache", buildCacheProbe)
	f.setRegression(check("reg", buildCacheProbe, true))
	res := f.complete(f.claim(string(id)))
	if !res.Completed {
		t.Fatalf("snapshot checks did not see the build-cache variables: %s", res.Summary)
	}
}
