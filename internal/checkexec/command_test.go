package checkexec

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func sh(script string) []string { return []string{"sh", "-c", script} }

func TestRunExitCodesAndStreams(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	r := Run(context.Background(), Spec{Argv: sh("echo out; echo err >&2; exit 3"), Timeout: 5 * time.Second})
	if !r.Started || r.ExitCode != 3 || r.TimedOut || strings.TrimSpace(r.Stdout) != "out" || strings.TrimSpace(r.Stderr) != "err" {
		t.Fatalf("%+v", r)
	}
	ok := Run(context.Background(), Spec{Argv: []string{"true"}})
	if !ok.Started || ok.ExitCode != 0 || ok.Err != nil {
		t.Fatalf("%+v", ok)
	}
	if ok.FinishedAt.Before(ok.StartedAt) {
		t.Fatal("timestamps")
	}
}

func TestRunMissingBinaryIsNotStarted(t *testing.T) {
	r := Run(context.Background(), Spec{Argv: []string{"definitely-not-a-real-binary-xyz"}})
	if r.Started || r.ExitCode != -1 || r.Err == nil {
		t.Fatalf("%+v", r)
	}
	e := Run(context.Background(), Spec{Argv: nil})
	if e.Started || e.Err == nil {
		t.Fatalf("%+v", e)
	}
}

func TestRunTimeoutKills(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	start := time.Now()
	r := Run(context.Background(), Spec{Argv: sh("echo started; sleep 30; echo late"), Timeout: 300 * time.Millisecond})
	if time.Since(start) > 10*time.Second {
		t.Fatal("did not return promptly")
	}
	if !r.TimedOut || r.ExitCode == 0 || !strings.Contains(r.Stdout, "started") || strings.Contains(r.Stdout, "late") {
		t.Fatalf("%+v", r)
	}
}

func TestRunDirAndBoundedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	dir := t.TempDir()
	r := Run(context.Background(), Spec{Argv: sh("pwd"), Dir: dir})
	got, _ := filepath.EvalSymlinks(strings.TrimSpace(r.Stdout))
	want, _ := filepath.EvalSymlinks(dir)
	if got != want {
		t.Fatalf("cwd %q != %q", got, want)
	}
	big := Run(context.Background(), Spec{Argv: sh("head -c 200000 /dev/zero | tr '\\0' 'a'; echo END")})
	if len(big.Stdout) > OutputLimit+200 || !strings.Contains(big.Stdout, "truncated") || !strings.HasSuffix(strings.TrimSpace(big.Stdout), "END") {
		t.Fatalf("len=%d tail=%q", len(big.Stdout), big.Stdout[len(big.Stdout)-40:])
	}
	bad := Run(context.Background(), Spec{Argv: []string{"true"}, Dir: filepath.Join(dir, "missing")})
	if bad.Started || bad.Err == nil {
		t.Fatalf("%+v", bad)
	}
}
