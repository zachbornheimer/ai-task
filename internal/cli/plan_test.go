package cli

import (
	"strings"
	"testing"
)

func TestGroupRejectsTaskOnlyFlags(t *testing.T) {
	taskOnly := []struct {
		name string
		args []string
	}{
		{"requires", []string{"--requires", "x"}},
		{"blocks", []string{"--blocks", "x"}},
		{"accept", []string{"--accept", "done"}},
		{"constraint", []string{"--constraint", "stay on the path"}},
		{"pin", []string{"--pin", "secret.go"}},
		{"cohort", []string{"--cohort", "ship"}},
		{"outcome", []string{"--outcome", "shipped"}},
		{"on-complete-json", []string{"--on-complete-json", "{}"}},
		{"claim", []string{"--claim"}},
	}
	for _, tc := range taskOnly {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"add", "Epic", "--group"}, tc.args...)
			code, out, errOut := runAt(t, args...)
			if code != ExitUsage {
				t.Fatalf("add --group %s: exit %d (want usage)\nstdout:\n%s\nstderr:\n%s", tc.name, code, out, errOut)
			}
			if !strings.Contains(errOut, "usage error") || !strings.Contains(errOut, "--group") {
				t.Fatalf("add --group %s: want a --group usage error, got:\n%s", tc.name, errOut)
			}
		})
	}

	code, out, errOut := runAt(t, "add", "Epic", "--group", "--key", "epic", "--check", "u: true")
	if code != ExitOK {
		t.Fatalf("add --group with group flags: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}
