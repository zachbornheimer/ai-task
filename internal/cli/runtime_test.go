//go:build !windows

package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
)

const (
	testRuntimeSession     = "interactive/session/registry-test"
	testRuntimeIncarnation = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testRuntimeCapability  = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
)

func runtimeFixture(t *testing.T) (string, string, runtimeRegistration) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(testRuntimeSession))
	name := filepath.Join(root, hex.EncodeToString(digest[:])+".json")
	registration := runtimeRegistration{Session: testRuntimeSession, PID: os.Getppid(), Incarnation: testRuntimeIncarnation, Capability: testRuntimeCapability}
	writeRuntimeFixture(t, name, registration)
	return root, name, registration
}

func writeRuntimeFixture(t *testing.T, name string, registration runtimeRegistration) {
	t.Helper()
	raw, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func runtimeFixtureEnv(root, session string) func(string) string {
	return func(key string) string {
		switch key {
		case runtimeRootEnv:
			return root
		case runtimeSessionEnv:
			return session
		}
		return ""
	}
}

func TestRuntimeRegistryPrivateRegistration(t *testing.T) {
	root, _, registration := runtimeFixture(t)
	identity, err := lookupRuntime(context.Background(), runtimeFixtureEnv(root, testRuntimeSession))
	if err != nil || identity == nil {
		t.Fatalf("lookup private host: identity=%v err=%v", identity, err)
	}
	capability, err := hex.DecodeString(registration.Capability)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(capability)
	if identity.Session != registration.Session || identity.PID != registration.PID || identity.Incarnation != registration.Incarnation || identity.CapabilityDigest != hex.EncodeToString(digest[:]) {
		t.Fatalf("runtime identity does not match registration")
	}
	raw, err := json.Marshal(identity)
	if err != nil || bytes.Contains(raw, []byte(testRuntimeCapability)) {
		t.Fatalf("identity serialization exposes capability or fails: %v", err)
	}
}

func TestRuntimeRegistryMissingDoesNotAuthorize(t *testing.T) {
	root, name, _ := runtimeFixture(t)
	for _, tc := range []struct{ name, root, session string }{
		{"no environment", "", ""},
		{"session alone", "", testRuntimeSession},
		{"root alone", root, ""},
		{"different session", root, "native-subagent-session"},
		{"missing root", filepath.Join(root, "absent"), testRuntimeSession},
	} {
		t.Run(tc.name, func(t *testing.T) {
			identity, err := lookupRuntime(context.Background(), runtimeFixtureEnv(tc.root, tc.session))
			if err != nil || identity != nil {
				t.Fatalf("missing registration must be unbound: identity=%v err=%v", identity, err)
			}
		})
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	identity, err := lookupRuntime(context.Background(), runtimeFixtureEnv(root, testRuntimeSession))
	if err != nil || identity != nil {
		t.Fatalf("removed registration must be unbound: identity=%v err=%v", identity, err)
	}
}

func TestRuntimeRegistryRejectsMalformedIdentity(t *testing.T) {
	cases := []struct {
		name   string
		change func(*runtimeRegistration)
	}{
		{"session mismatch", func(r *runtimeRegistration) { r.Session = "other-session" }},
		{"zero PID", func(r *runtimeRegistration) { r.PID = 0 }},
		{"negative PID", func(r *runtimeRegistration) { r.PID = -1 }},
		{"self is not host ancestor", func(r *runtimeRegistration) { r.PID = os.Getpid() }},
		{"missing incarnation", func(r *runtimeRegistration) { r.Incarnation = "" }},
		{"short incarnation", func(r *runtimeRegistration) { r.Incarnation = "ff" }},
		{"nonhex incarnation", func(r *runtimeRegistration) { r.Incarnation = strings.Repeat("z", 64) }},
		{"missing capability", func(r *runtimeRegistration) { r.Capability = "" }},
		{"short capability", func(r *runtimeRegistration) { r.Capability = "ff" }},
		{"nonhex capability", func(r *runtimeRegistration) { r.Capability = strings.Repeat("z", 64) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, name, registration := runtimeFixture(t)
			tc.change(&registration)
			writeRuntimeFixture(t, name, registration)
			identity, err := lookupRuntime(context.Background(), runtimeFixtureEnv(root, testRuntimeSession))
			if err == nil || identity != nil {
				t.Fatalf("invalid identity was accepted")
			}
			if strings.Contains(err.Error(), testRuntimeCapability) {
				t.Fatal("validation error leaked capability")
			}
		})
	}
}

func TestRuntimeRegistryRejectsInsecureFilesystem(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, string, string) string
	}{
		{"public root", func(t *testing.T, root, name string) string { mustRuntimeChmod(t, root, 0755); return root }},
		{"public file", func(t *testing.T, root, name string) string { mustRuntimeChmod(t, name, 0644); return root }},
		{"executable file", func(t *testing.T, root, name string) string { mustRuntimeChmod(t, name, 0700); return root }},
		{"root is regular file", func(t *testing.T, root, name string) string { return name }},
		{"relative root", func(t *testing.T, root, name string) string { return "relative-runtime-root" }},
		{"trailing separator", func(t *testing.T, root, name string) string { return root + string(os.PathSeparator) }},
		{"symlink root", func(t *testing.T, root, name string) string {
			link := filepath.Join(t.TempDir(), "runtime-link")
			if err := os.Symlink(root, link); err != nil {
				t.Fatal(err)
			}
			return link
		}},
		{"symlink file", func(t *testing.T, root, name string) string {
			target := filepath.Join(root, "target")
			if err := os.Rename(name, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, name); err != nil {
				t.Fatal(err)
			}
			return root
		}},
		{"directory registration", func(t *testing.T, root, name string) string {
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(name, 0700); err != nil {
				t.Fatal(err)
			}
			return root
		}},
		{"malformed JSON", func(t *testing.T, root, name string) string {
			if err := os.WriteFile(name, []byte(`{"capability":"`+testRuntimeCapability+`",BROKEN}`), 0600); err != nil {
				t.Fatal(err)
			}
			return root
		}},
		{"oversized JSON", func(t *testing.T, root, name string) string {
			if err := os.WriteFile(name, []byte(strings.Repeat(" ", runtimeRegistrationLimit+1)), 0600); err != nil {
				t.Fatal(err)
			}
			return root
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, name, _ := runtimeFixture(t)
			root = tc.change(t, root, name)
			identity, err := lookupRuntime(context.Background(), runtimeFixtureEnv(root, testRuntimeSession))
			if err == nil || identity != nil {
				t.Fatalf("unsafe registry was accepted")
			}
			if strings.Contains(err.Error(), testRuntimeCapability) {
				t.Fatal("filesystem error leaked capability")
			}
		})
	}
}

func mustRuntimeChmod(t *testing.T, path string, mode fs.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeRegistryRejectsUnownedPaths(t *testing.T) {
	root, name, _ := runtimeFixture(t)
	for _, tc := range []struct {
		path      string
		directory bool
	}{{root, true}, {name, false}} {
		info, err := os.Lstat(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		foreign := &runtimeForeignFileInfo{FileInfo: info, stat: *info.Sys().(*syscall.Stat_t)}
		foreign.stat.Uid = uint32(os.Getuid()) + 1
		if err := privateRuntimePath(foreign, tc.directory); err == nil {
			t.Fatal("foreign-owned registry accepted")
		}
	}
}

type runtimeForeignFileInfo struct {
	fs.FileInfo
	stat syscall.Stat_t
}

func (f *runtimeForeignFileInfo) Sys() any { return &f.stat }

func TestRuntimeRegistryRejectsLiveSiblingAndDeadOwner(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimeRegistrySleepingChild$")
	cmd.Env = append(os.Environ(), "ATASK_RUNTIME_TEST_CHILD=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	root, name, registration := runtimeFixture(t)
	registration.PID = cmd.Process.Pid
	writeRuntimeFixture(t, name, registration)
	identity, err := lookupRuntime(context.Background(), runtimeFixtureEnv(root, testRuntimeSession))
	if err == nil || identity != nil {
		t.Fatal("live non-ancestor was authorized")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	identity, err = lookupRuntime(context.Background(), runtimeFixtureEnv(root, testRuntimeSession))
	if err == nil || identity != nil {
		t.Fatal("dead owner was authorized")
	}
}

func TestRuntimeRegistrySleepingChild(t *testing.T) {
	if os.Getenv("ATASK_RUNTIME_TEST_CHILD") != "1" {
		return
	}
	time.Sleep(time.Minute)
}

func TestRuntimeRegistryProcParent(t *testing.T) {
	for _, tc := range []struct {
		stat    string
		want    int
		invalid bool
	}{
		{"123 (host command) S 42 0 0", 42, false},
		{"123 (host (command) with) parens) S 43 0", 43, false},
		{"123 (host) Z 42", 0, true},
		{"123 (host) X 42", 0, true},
		{"123 no-parenthesis S 42", 0, true},
		{"123 (host) S -2", 0, true},
	} {
		parent, err := runtimeProcParent(tc.stat, 123)
		if (err != nil) != tc.invalid || parent != tc.want {
			t.Fatalf("proc metadata: parent=%d err=%v", parent, err)
		}
	}
}

func TestRuntimeRegistryEngineConfigMerge(t *testing.T) {
	root, _, _ := runtimeFixture(t)
	cwd := t.TempDir()
	cfg := app.Config{Path: "unused-path", Actor: "unused-actor", Cwd: "unused-cwd", Now: func() time.Time { return time.Unix(100, 0) }, Lease: time.Second}
	c := &ctxt{env: Env{Cwd: cwd, Getenv: runtimeFixtureEnv(root, testRuntimeSession), engineConfig: &cfg}, g: globals{db: ":memory:"}}
	engine, err := c.engine(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if cfg.Path != "unused-path" || cfg.Cwd != "unused-cwd" || cfg.Actor != "unused-actor" || cfg.Runtime != nil {
		t.Fatal("CLI modified caller's injected config")
	}
	if again, err := c.engine(context.Background()); err != nil || again != engine {
		t.Fatal("CLI did not retain opened engine")
	}
}
