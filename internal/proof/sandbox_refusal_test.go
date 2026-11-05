package proof

import (
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

func TestSandboxRefusalNamesTheCredentialACheckCannotReach(t *testing.T) {
	refused := []struct{ name, subject, wantInMessage string }{
		{"plain ssh", "ssh mini true", "~/.ssh"},
		{"jump host with a quoted remote command", `ssh -J mini zbornheimer@backend.ai.lan 'grep -c conversation ~/x.ts'`, "~/.ssh"},
		{"inside bash -c after cd", `bash -c 'cd /repo && ssh h true'`, "~/.ssh"},
		{"inside sh -lc", `sh -lc "scp a h:b"`, "~/.ssh"},
		{"absolute path", "/usr/bin/ssh h true", "~/.ssh"},
		{"after an assignment", "FOO=1 ssh h true", "~/.ssh"},
		{"through env", "env -i ssh h true", "~/.ssh"},
		{"through timeout", "timeout 20 ssh h true", "~/.ssh"},
		{"as a condition", "if ssh h true; then exit 0; fi", "~/.ssh"},
		{"at the end of a pipe", "echo x | ssh h cat", "~/.ssh"},
		{"in a command substitution", "test $(ssh h hostname) = h", "~/.ssh"},
		{"sftp", "sftp h", "~/.ssh"},
		{"nested shells", `bash -c "sh -c 'ssh h true'"`, "~/.ssh"},
		{"op without the token grant", "op read op://v/i/credential", "OP_SERVICE_ACCOUNT_TOKEN"},
		{"op in a shell script", `bash -c 'x=$(op read op://v/i/f) && test -n "$x"'`, "extra_env"},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			err := SandboxRefusal(c.subject, nil)
			if fault.CodeOf(err) != fault.CodeInvalidInput {
				t.Fatalf("SandboxRefusal(%q) = %v, want INVALID_INPUT", c.subject, err)
			}
			if !strings.Contains(err.Error(), c.wantInMessage) {
				t.Fatalf("message %q does not name %q", err, c.wantInMessage)
			}
		})
	}
}

func TestSandboxRefusalLeavesOrdinaryChecksAlone(t *testing.T) {
	for _, subject := range []string{
		"go test ./...",
		"grep -c ssh notes.txt",
		"echo ssh",
		"cat ssh.go",
		"mise run ssh:check",
		"git archive main f.ts | tar -xO | grep -qc --conversation",
		`bash -c 'cd /repo && git archive main mastra/src/dev_env.ts | tar -xO | grep -qc -- --conversation'`,
		"test -f ~/.ssh/config",
		"ls opt",
		"",
	} {
		if err := SandboxRefusal(subject, nil); err != nil {
			t.Errorf("SandboxRefusal(%q) = %v, want nil", subject, err)
		}
	}
}

// op is refused only while the owner has not granted the token: with it, the
// check can log in headlessly, so filing it is right.
func TestSandboxRefusalAcceptsOpWhenTheTokenIsGranted(t *testing.T) {
	granted := []string{"GITHUB_TOKEN", OPTokenEnv}
	if err := SandboxRefusal("op read op://v/i/credential", granted); err != nil {
		t.Fatalf("op with %s granted was refused: %v", OPTokenEnv, err)
	}
	if err := SandboxRefusal("ssh h true", granted); err == nil {
		t.Fatal("ssh must stay refused: no extra_env entry grants ~/.ssh")
	}
}
