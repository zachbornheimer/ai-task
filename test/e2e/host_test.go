package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGrokHostUsesHeadlessFlags(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "hosts", "grok.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"--output-format plain", "-p", "--always-approve", "common.sh"} {
		if !strings.Contains(s, want) {
			t.Fatalf("grok.sh lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "--output-format text") || strings.Contains(s, `"${GROK_ARGS:-}"`) {
		t.Fatalf("grok.sh still uses the old invocation:\n%s", s)
	}
}

func TestUnscopedHostLoopCompletes(t *testing.T) {
	script := filepath.Join("..", "..", "examples", "hosts", "smoke-unscoped-host.sh")
	cmd := exec.Command("sh", script)
	cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"), "HK=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("smoke-unscoped-host: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "HARNESS_INVOKED") || !strings.Contains(string(out), "done: every task is complete") {
		t.Fatalf("unscoped host loop:\n%s", out)
	}
}

func TestGrokHeadlessPrompt(t *testing.T) {
	if _, err := exec.LookPath("grok"); err != nil {
		t.Skip("grok CLI not on PATH")
	}
	ctx := t.Context()
	cmd := exec.CommandContext(ctx, "grok", "--always-approve", "--output-format", "plain", "--no-auto-update", "-p", "Reply with exactly the word PONG and nothing else.")
	cmd.Env = os.Environ()
	done := make(chan error, 1)
	var out []byte
	go func() {
		var err error
		out, err = cmd.CombinedOutput()
		done <- err
	}()
	select {
	case err := <-done:
		got := string(out)
		if err != nil {
			if strings.Contains(got, "login") || strings.Contains(got, "API") || strings.Contains(got, "auth") {
				t.Skipf("grok not authenticated: %v\n%s", err, got)
			}
			t.Fatalf("grok -p: %v\n%s", err, got)
		}
		if !strings.Contains(strings.ToUpper(got), "PONG") {
			t.Fatalf("grok -p did not return PONG:\n%s", got)
		}
	case <-time.After(90 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("grok -p timed out")
	}
}
