package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
)

func TestRuntimeClaimOutputKeepsPrivateTokenOutOfModelContext(t *testing.T) {
	for _, format := range []string{"human", "json"} {
		for _, branch := range []string{"", "atask/at-bound1"} {
			t.Run(format+"/"+branch, func(t *testing.T) {
				var out bytes.Buffer
				c := &ctxt{env: Env{Stdout: &out}, g: globals{json: format == "json"}, runtimeBound: true}
				session := app.Session{Token: execution.Token("private-test-token"), Branch: branch}
				if err := c.emitSession(session); err != nil {
					t.Fatal(err)
				}
				if branch == "" && strings.Contains(out.String(), string(session.Token)) {
					t.Fatal("runtime token leaked")
				}
				if branch != "" && !strings.Contains(out.String(), string(session.Token)) {
					t.Fatal("private-worktree explicit token contract changed")
				}
				if format == "json" {
					var result struct {
						Result map[string]any `json:"result"`
					}
					if err := json.Unmarshal(out.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					_, hasToken := result.Result["token"]
					if hasToken != (branch != "") {
						t.Fatalf("token presence=%v branch=%q", hasToken, branch)
					}
				}
				if session.Token != "private-test-token" {
					t.Fatal("response redaction mutated session")
				}
			})
		}
	}
}
