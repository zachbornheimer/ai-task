package e2e

import (
	"strings"
	"testing"
)

// TestCheckWithShellOperatorsIsRejected: checks run without a shell, so a
// check whose words include a bare shell operator would silently pass its
// operator to the program as an argument. It is rejected with INVALID_INPUT
// that says to wrap it in sh -c; quoted operators stay allowed.
func TestCheckWithShellOperatorsIsRejected(t *testing.T) {
	e := gitEnv(t)
	for _, op := range []string{"&&", "||", "|", ";", ">", ">>", "<"} {
		cmd := "echo one " + op + " echo two"
		for name, args := range map[string][]string{
			"flag":     {"add", "T", "--check", "u: " + cmd},
			"optional": {"add", "T", "--check", "ok: true", "--optional-check", "o: " + cmd},
			"plan":     {"add", "--plan", `{"key":"p","title":"P","checks":["u: ` + cmd + `"]}`},
		} {
			r := e.fails("INVALID_INPUT", args...)
			msg, _ := r.env["error"].(map[string]any)["message"].(string)
			if !strings.Contains(msg, "sh -c") || !strings.Contains(msg, "without a shell") || !strings.Contains(msg, "'"+op+"'") {
				t.Fatalf("%s %q: message should name the operator, say checks run without a shell and suggest sh -c: %s", name, op, msg)
			}
		}
	}
	// A rejected plan creates nothing.
	tasks, ok := e.ok("list", "open")["tasks"].([]any)
	if !ok {
		t.Fatal("list open: missing tasks")
	}
	if n := len(tasks); n != 0 {
		t.Fatalf("rejected checks must not create tasks, got %d", n)
	}
	// Wrapped in sh -c, or with the operator quoted inside an argument,
	// the check is fine.
	e.ok("add", "Wrapped", "--key", "wrapped", "--check", "u: sh -c 'test -f a.txt && test -f b.txt'")
	e.ok("add", "Quoted", "--key", "quoted", "--check", `u: echo '&&' "|" ';' a>b`)
	e.ok("add", "--plan", `{"key":"planned","title":"Planned","checks":["u: sh -c 'true || false'"]}`)
}
