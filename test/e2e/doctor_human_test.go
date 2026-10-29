package e2e

import (
	"strings"
	"testing"
)

// TestDoctorHumanReportOnFailure: without ATASK_OUTPUT=json an unhealthy
// `atask doctor` prints the whole report (every check, the fix for the
// failing one) on stderr, not only the one-line error; the JSON envelope is
// unchanged.
func TestDoctorHumanReportOnFailure(t *testing.T) {
	// A directory that is not a Git work tree: no task filed here can get a
	// worktree, so the repository is not ready.
	e := newEnv(t)

	r := e.runIn(e.cwd, "", []string{"ATASK_OUTPUT="}, "doctor")
	if r.code == 0 {
		t.Fatalf("doctor succeeded outside a repository: %s", r.stdout)
	}
	for _, want := range []string{"error [NOT_READY]", "repository", "fix:", "○ not ready"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.stderr)
		}
	}
	if strings.TrimSpace(r.stdout) != "" {
		t.Errorf("human failure wrote to stdout: %s", r.stdout)
	}

	j := e.fails("NOT_READY", "doctor")
	det, _ := j.env["error"].(map[string]any)["details"].(map[string]any)
	if det == nil || det["healthy"] != false || j.stderr != "" {
		t.Fatalf("JSON doctor changed: stdout=%s stderr=%s", j.stdout, j.stderr)
	}
}
