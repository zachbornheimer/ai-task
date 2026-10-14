package e2e

import (
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/must"
)

// TestSubcommandHelpListsFlags: `atask <command> --help` (and -h) is a request
// for help, not a usage error: it prints the command's usage line and every
// flag with its description on stdout and exits 0; in JSON mode the same
// content arrives in an envelope.
func TestSubcommandHelpListsFlags(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		args  []string
		flags []string
	}{
		{[]string{"add", "--help"}, []string{"-key", "stable project-local key", "-group", "-proof"}},
		{[]string{"log", "-h"}, []string{"-done", "progress made", "-next", "-learned"}},
		{[]string{"claim", "--help"}, []string{"-wait", "block until work is claimable"}},
		{[]string{"claim", "release", "--help"}, []string{"-note", "-failed"}},
		{[]string{"verify", "--help"}, []string{"-reverify"}},
	} {
		// Text mode (ATASK_OUTPUT unset): plain help on stdout, nothing on stderr.
		r := e.runIn(e.cwd, "", []string{"ATASK_OUTPUT="}, tc.args...)
		if r.code != 0 || strings.TrimSpace(r.stderr) != "" || !strings.Contains(r.stdout, "Usage: ") || !strings.Contains(r.stdout, "atask "+tc.args[0]) {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", tc.args, r.code, r.stdout, r.stderr)
		}
		for _, want := range tc.flags {
			if !strings.Contains(r.stdout, want) {
				t.Fatalf("%v: help does not list %q:\n%s", tc.args, want, r.stdout)
			}
		}
		// JSON mode: the same help in an envelope.
		j := e.ok(tc.args...)
		usage, _ := j["usage"].(string)
		flags, _ := j["flags"].([]any)
		if !strings.Contains(usage, tc.args[0]) || len(flags) == 0 {
			t.Fatalf("%v: help envelope: %v", tc.args, j)
		}
		found := map[string]string{}
		for _, f := range flags {
			m := must.As[map[string]any](f)
			found[m["name"].(string)], _ = m["usage"].(string)
		}
		for _, want := range tc.flags {
			if n := strings.TrimPrefix(want, "-"); n != want && found[n] == "" {
				t.Fatalf("%v: envelope lacks flag %q with a description: %v", tc.args, n, j)
			}
		}
	}
	// A real mistake is still a usage error.
	e.fails("INVALID_INPUT", "add", "--no-such-flag")
	// The top-level help names no removed verb.
	if r := e.runIn(e.cwd, "", []string{"ATASK_OUTPUT="}, "help"); strings.Contains(r.stdout+r.stderr, "verify groups") || strings.Contains(r.stdout+r.stderr, "hook") {
		t.Fatalf("at help names a removed verb: %s%s", r.stdout, r.stderr)
	}
}
