package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const deniedMessage = "Operation not permitted"

func requireSandboxExec(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt is macOS only")
	}
	if _, err := exec.LookPath(sandboxExecBinary); err != nil {
		t.Skip("sandbox-exec not installed")
	}
}

func runIn(sb *Sandbox, dir string, env []string, script string) Result {
	return Run(context.Background(), Spec{Argv: sh(script), Dir: dir, Env: env, Timeout: time.Minute, Sandbox: sb})
}

func TestSandboxDeniesHomeWriteAndSecretRead(t *testing.T) {
	if Inherited() {
		// atask verify already confines this test: assert the ambient
		// confinement instead of nesting a second sandbox.
		home, _ := os.UserHomeDir()
		canary := filepath.Join(home, "atask-canary-ambient")
		defer func() { _ = os.Remove(canary) }()
		r := Run(context.Background(), Spec{Argv: sh(`echo x > "` + canary + `"`)})
		if r.ExitCode == 0 || !strings.Contains(r.Stderr, deniedMessage) {
			t.Fatalf("ambient sandbox allowed a home write: %+v", r)
		}
		return
	}
	requireSandboxExec(t)
	home := t.TempDir()
	work, secrets := filepath.Join(home, "work"), filepath.Join(home, ".ssh")
	for _, d := range []string{work, secrets} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	key := filepath.Join(secrets, "id_test")
	if err := os.WriteFile(key, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := &Sandbox{Mode: SandboxStrict, Home: home, Writable: []string{work}, Unreadable: []string{secrets}}
	env := append(os.Environ(), "HOME="+home)

	r := runIn(sb, work, env, `echo x > "$HOME/atask-canary"`)
	if !r.Sandboxed || r.ExitCode == 0 || !strings.Contains(r.Stderr, deniedMessage) {
		t.Fatalf("home write was not denied: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(home, "atask-canary")); err == nil {
		t.Fatal("canary file exists")
	}

	r = runIn(sb, work, env, `cat "`+key+`"`)
	if r.ExitCode == 0 || strings.Contains(r.Stdout, "secret") || !strings.Contains(r.Stderr, deniedMessage) {
		t.Fatalf("secret read was not denied: %+v", r)
	}

	if r = runIn(sb, work, env, `echo ok > "`+filepath.Join(work, "file")+`"`); r.ExitCode != 0 {
		t.Fatalf("writable path denied: %+v", r)
	}
}

func TestSandboxAllowsToolchainAndGitCommit(t *testing.T) {
	requireSandboxExec(t)
	if Inherited() {
		t.Skip("covered by the ambient sandbox")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, tree := filepath.Join(root, "repo"), filepath.Join(root, "tree")
	isolatedGit := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir, cmd.Env = dir, isolatedGit
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatal(err)
	}
	git(repo, "init", "-q")
	git(repo, "commit", "-q", "--allow-empty", "-m", "base")
	git(repo, "worktree", "add", "-q", "-b", "work", tree)

	sb := &Sandbox{Mode: SandboxStrict, Home: root, Writable: []string{tree, filepath.Join(repo, ".git")}}
	script := `go version && echo y > f && git add f && git -c user.name=t -c user.email=t@t -c commit.gpgsign=false commit -q -m c`
	if r := runIn(sb, tree, isolatedGit, script); r.ExitCode != 0 {
		t.Fatalf("toolchain or git commit denied: %+v", r)
	}
}

func TestStrictSandboxFailsClosedWithoutTool(t *testing.T) {
	t.Setenv(SandboxEnvVar, "")
	sb := &Sandbox{Mode: SandboxStrict, GOOS: "darwin", LookPath: func(string) (string, error) { return "", errors.New("nope") }}
	r := Run(context.Background(), Spec{Argv: []string{"true"}, Sandbox: sb})
	if r.Started || !errors.Is(r.Err, ErrSandboxUnavailable) {
		t.Fatalf("%+v", r)
	}
	if !errors.Is(sb.Check(), ErrSandboxUnavailable) {
		t.Fatal("Check must fail closed")
	}
}

func TestNoneModeAndOtherPlatformsRunUnconfined(t *testing.T) {
	for _, sb := range []*Sandbox{{Mode: SandboxNone}, {Mode: SandboxStrict, GOOS: "linux"}} {
		r := Run(context.Background(), Spec{Argv: []string{"true"}, Sandbox: sb})
		if !r.Started || r.Sandboxed {
			t.Fatalf("%+v", r)
		}
	}
}

func TestProfileDeniesHomeWritesAndSecretReads(t *testing.T) {
	p := (&Sandbox{Home: "/h", Writable: []string{"/h/w"}, Unreadable: []string{"/h/.ssh"}}).Profile()
	for _, want := range []string{
		"(allow default)", `(deny file-write* (subpath "/h"))`,
		`(allow file-write* (subpath "/h/w"))`, `(deny file-read* (subpath "/h/.ssh"))`,
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("profile lacks %q:\n%s", want, p)
		}
	}
}

func TestProfileOrdersDeniesBeforeAllows(t *testing.T) {
	p := (&Sandbox{
		Home: "/h", Writable: []string{"/h/w"}, Readable: []string{"/h/s/snap"},
		Unreadable: []string{"/h/s/logs"}, UnreadablePrefixes: []string{"/h/s/atask.db"},
	}).Profile()
	deny := `(deny file-read* (regex "^(/h/s/atask\\.db)"))`
	if !strings.Contains(p, deny) {
		t.Fatalf("profile lacks %q:\n%s", deny, p)
	}
	lastDeny := strings.LastIndex(p, "(deny ")
	for _, allow := range []string{`(allow file-write* (subpath "/h/w"))`, `(allow file-read* (subpath "/h/s/snap"))`} {
		if i := strings.Index(p, allow); i < lastDeny {
			t.Fatalf("%q precedes a deny:\n%s", allow, p)
		}
	}
}
