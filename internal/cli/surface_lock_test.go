package cli

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

var cliFlagLine = regexp.MustCompile(`(?m)^\s+-([A-Za-z0-9][A-Za-z0-9-]*)`)

func TestCLITouchableSurface(t *testing.T) {
	wantVerbs := []string{
		"add", "claim", "context", "doctor", "help", "hook", "init", "list", "log",
		"project", "projects", "prune", "show", "status", "update", "verify", "version",
		"whoami",
	}
	gotVerbs := make([]string, 0, len(commands))
	for n := range commands {
		gotVerbs = append(gotVerbs, n)
	}
	assertExact(t, "CLI verbs", wantVerbs, gotVerbs)

	globals := []string{"db", "json", "output", "pretty", "project"}
	commandFlags := map[string][]string{
		"add": {
			"accept", "blocks", "check", "claim", "cohort", "constraint", "expect-rev",
			"f", "group", "idempotency-key", "key", "optional-check", "outcome", "parent",
			"pin", "plan", "planner", "policy-json", "requires", "session", "size",
		},
		"update": {
			"accept", "archive", "check", "cohort", "constraint", "expect-rev",
			"idempotency-key", "optional-check", "outcome", "parent", "pin",
			"planner", "reason", "remove-requires", "requires", "reset-attempts",
			"session", "set-requires", "size", "title", "withdraw-submission",
		},
		"show":     {"full"},
		"claim":    {"lease", "wait"},
		"log":      {"done", "learned", "next", "note", "session"},
		"verify":   {"session"},
		"init":     {"claude", "name", "no-instructions", "path"},
		"doctor":   {"skip-baseline"},
		"context":  {"session", "with-rules"},
		"whoami":   {"session"},
		"help":     nil,
		"hook":     nil,
		"version":  nil,
		"list":     nil,
		"status":   nil,
		"projects": nil,
		"prune":    nil,
		"project":  nil, // trusted project flags are project configuration, not the agent verb set
	}

	for _, n := range wantVerbs {
		if _, ok := commands[n]; !ok {
			continue
		}
		code, out, errOut := runAt(t, n, "--help")
		if code != ExitOK {
			t.Errorf("atask %s --help: exit %d\n%s\n%s", n, code, out, errOut)
			continue
		}
		got := flagsFromHelp(out)
		specific, listed := commandFlags[n]
		if !listed {
			continue
		}
		want := append([]string{}, globals...)
		want = append(want, specific...)
		if n == "project" {
			assertContains(t, "atask project globals", globals, got)
			continue
		}
		assertExact(t, "atask "+n+" flags", want, got)
	}

	wantListFilters := []string{"all", "archived", "blocked", "open", "ready"}
	var gotFilters []string
	for _, f := range []string{"all", "archived", "blocked", "complete", "open", "ready", "whoami", "hook"} {
		if _, ok := listFilter(f); ok {
			gotFilters = append(gotFilters, f)
		}
	}
	assertExact(t, "list filters", wantListFilters, gotFilters)

	claimHelp := commands["claim"].summary
	for _, sub := range []string{"renew", "release"} {
		if !strings.Contains(claimHelp, "claim "+sub) {
			t.Errorf("claim surface missing subcommand %s (summary %q)", sub, claimHelp)
		}
	}
	verifyHelp := commands["verify"].summary
	for _, mode := range []string{"task", "regression", "complete"} {
		if !strings.Contains(verifyHelp, mode) {
			t.Errorf("verify surface missing mode %s (summary %q)", mode, verifyHelp)
		}
	}
}

func flagsFromHelp(help string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range cliFlagLine.FindAllStringSubmatch(help, -1) {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func assertExact(t *testing.T, label string, want, got []string) {
	t.Helper()
	w, g := setOf(want), setOf(got)
	var missing, extra []string
	for k := range w {
		if !g[k] {
			missing = append(missing, k)
		}
	}
	for k := range g {
		if !w[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) == 0 && len(extra) == 0 {
		return
	}
	t.Errorf("%s: missing %v extra %v\n  want %v\n  got  %v", label, missing, extra, sorted(want), sorted(got))
}

func assertContains(t *testing.T, label string, want, got []string) {
	t.Helper()
	g := setOf(got)
	var missing []string
	for _, k := range want {
		if !g[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%s: missing %v (got %v)", label, missing, sorted(got))
	}
}

func setOf(v []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range v {
		m[s] = true
	}
	return m
}

func sorted(v []string) []string {
	out := append([]string{}, v...)
	sort.Strings(out)
	return out
}
