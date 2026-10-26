package cli

import (
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	at "github.com/zachbornheimer/ai-task"
	"github.com/zachbornheimer/ai-task/internal/contract"
)

// contractSurface reads docs/contract.md, the one source of the public surface.
func contractSurface(t *testing.T) contract.Surface {
	t.Helper()
	s, err := contract.Parse(filepath.Join("..", "..", "docs", "contract.md"))
	if err != nil {
		t.Fatalf("parse contract: %v", err)
	}
	return s
}

// Each verb or flag outside docs/contract.md is rejected as INVALID_INPUT, and
// the message names the contract alternative where one exists.
func TestRemovedSurfaceRejected(t *testing.T) {
	h := newProofHarness(t)
	proofs := []string{"--proof", "run: go version", "--regression", "run: go version"}
	cases := []struct {
		args []string
		want string // the contract alternative the message must name
	}{
		{[]string{"hook", "session-start"}, "atask context"},
		{[]string{"init", "--claude"}, "AGENTS.md"},
		{[]string{"init", "--no-instructions"}, "AGENTS.md"},
		{[]string{"add", "t", "--check", "x: y"}, "--proof"},
		{[]string{"add", "t", "--optional-check", "x: y"}, "--proof"},
		{[]string{"add", "t", "--cohort", "c"}, "--group"},
		{[]string{"add", "t", "--pin", "p"}, "--constraint"},
		{[]string{"add", "--plan", "{}"}, "-f"},
		{[]string{"add", "t", "--policy-json", "{}"}, "--proof"},
		{[]string{"add", "t", "--expect-rev", "1"}, "ChangeSet"},
		{[]string{"add", "t", "--idempotency-key", "k"}, "ChangeSet"},
		{[]string{"add", "t", "--session", "s"}, "ATASK_SESSION"},
		{[]string{"add", "t", "--claim"}, "atask claim"},
		{[]string{"update", "x", "--check", "x: y"}, "--proof"},
		{[]string{"update", "x", "--optional-check", "x: y"}, "--proof"},
		{[]string{"update", "x", "--cohort", "c"}, "--group"},
		{[]string{"update", "x", "--pin", "p"}, "--constraint"},
		{[]string{"update", "x", "--set-requires", "y"}, "--remove-requires"},
		{[]string{"update", "x", "--session", "s"}, "ATASK_SESSION"},
		{[]string{"claim", "--lease", "10m"}, "claim renew"},
		{[]string{"log", "--session", "s"}, "ATASK_SESSION"},
		{[]string{"verify", "--session", "s", "complete"}, "ATASK_SESSION"},
		{[]string{"verify", "groups"}, "verify complete GROUP"},
		{[]string{"--output", "json", "list"}, "--json"},
		{[]string{"list", "--pretty"}, "--json"},
		{[]string{"project", "--regression-check", "x"}, "--regression"},
		{[]string{"project", "--integration", "promote"}, "on_complete"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			args := append([]string{"--json"}, tc.args...)
			if tc.args[0] == "add" && !slices.Contains(tc.args, "--plan") {
				args = append(args, proofs...)
			}
			if tc.args[0] == "--output" {
				args = append([]string{}, tc.args...)
				args = append([]string{"--json"}, args...)
			}
			code, out, errOut := h.run(args...)
			if code == ExitOK {
				t.Fatalf("expected a rejection, got success:\n%s", out)
			}
			all := out + errOut
			for _, want := range []string{"INVALID_INPUT", tc.want} {
				if !strings.Contains(all, want) {
					t.Fatalf("output lacks %q:\n%s", want, all)
				}
			}
		})
	}
}

// The registered verbs, per-verb flags, globals, list filters and Engine
// methods equal docs/contract.md in both directions.
func TestPublicSurfaceMatchesContract(t *testing.T) {
	s := contractSurface(t)

	docVerbs := map[string][]string{} // top-level verb -> its doc lines
	for _, v := range s.Verbs {
		top, _, _ := strings.Cut(v, " ")
		docVerbs[top] = append(docVerbs[top], v)
	}
	var wantVerbs, gotVerbs []string
	for v := range docVerbs {
		wantVerbs = append(wantVerbs, v)
	}
	for v := range commands {
		gotVerbs = append(gotVerbs, v)
	}
	assertExact(t, "CLI verbs", wantVerbs, gotVerbs)

	for _, v := range wantVerbs {
		wantFlags := map[string]bool{}
		gotFlags := map[string]bool{}
		for _, line := range docVerbs[v] {
			for _, f := range s.VerbFlags[line] {
				wantFlags[strings.TrimLeft(f, "-")] = true
			}
			args := append(strings.Fields(line), "--help")
			code, out, errOut := runAt(t, args...)
			if code != ExitOK {
				t.Fatalf("atask %s: exit %d\n%s\n%s", strings.Join(args, " "), code, out, errOut)
			}
			for _, f := range flagsFromHelp(out) {
				gotFlags[f] = true
			}
		}
		for _, g := range s.GlobalFlags {
			if !gotFlags[strings.TrimLeft(g, "-")] {
				t.Errorf("atask %s lacks global %s", v, g)
			}
			delete(gotFlags, strings.TrimLeft(g, "-"))
		}
		var want, got []string
		for f := range wantFlags {
			want = append(want, f)
		}
		for f := range gotFlags {
			got = append(got, f)
		}
		if v == "update" {
			// update has no use for the flags that create an item.
			got = append(got, "key", "group", "blocks")
		}
		assertExact(t, "atask "+v+" flags", want, got)
	}

	var gotFilters []string
	for _, f := range []string{"all", "open", "ready", "blocked", "draft", "complete", "archived", "whoami", "hook", "project"} {
		if _, ok := listFilter(f); ok {
			gotFilters = append(gotFilters, f)
		}
	}
	assertExact(t, "list filters", s.ListFilters, gotFilters)

	engine := reflect.TypeFor[at.Engine]()
	var methods []string
	for method := range engine.Methods() {
		methods = append(methods, method.Name)
	}
	sort.Strings(methods)
	assertExact(t, "Engine methods", s.EngineMethods, methods)
}

// A title that begins with -- is a title after the -- terminator.
func TestDashedTitleIsStoredAsTitle(t *testing.T) {
	h := newProofHarness(t)
	code, out, errOut := h.run("--json", "add", "--proof", "run: go version", "--regression", "run: go version", "--", "--not-a-flag")
	if code != ExitOK || !strings.Contains(out, `"title":"--not-a-flag"`) {
		t.Fatalf("add -- title: %d\n%s\n%s", code, out, errOut)
	}
}

// `atask help COMMAND` lists that command's flags and no other command's.
func TestHelpCommandListsOnlyThatCommandsFlags(t *testing.T) {
	code, out, errOut := runAt(t, "help", "claim")
	if code != ExitOK {
		t.Fatalf("help claim: %d %s", code, errOut)
	}
	got := flagsFromHelp(out)
	assertExact(t, "help claim flags", []string{"db", "group", "json", "project", "wait"}, got)
	code, out, errOut = runAt(t, "help", "add")
	if code != ExitOK || !strings.Contains(out, "-proof") || !strings.Contains(out, "run:") || strings.Contains(out, "-wait") {
		t.Fatalf("help add: %d\n%s\n%s", code, out, errOut)
	}
}
