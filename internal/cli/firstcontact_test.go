package cli

import (
	"bytes"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"testing"
)

func runAt(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	db := filepath.Join(t.TempDir(), "at.db")
	code := Main(Env{Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Cwd: t.TempDir(), Getenv: func(k string) string {
		if k == "ATASK_DB" {
			return db
		}
		return ""
	}})
	return code, out.String(), errb.String()
}

func TestTopLevelHelp(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		code, out, errOut := runAt(t, flag)
		if code != ExitOK || !strings.Contains(out, "Commands:") || !strings.Contains(out, "verify") || strings.Contains(errOut, "usage error") {
			t.Fatalf("at %s: exit %d\nstdout:\n%s\nstderr:\n%s", flag, code, out, errOut)
		}
	}
	// `atask help` is for reading (or paging): stdout, exit 0.
	if code, out, _ := runAt(t, "help"); code != ExitOK || !strings.Contains(out, "Commands:") {
		t.Fatalf("at help: exit %d\n%s", code, out)
	}
	// A bare `atask` lists every non-archived task (empty here).
	if code, out, errOut := runAt(t); code != ExitOK || !strings.Contains(out, "no tasks registered") {
		t.Fatalf("bare atask: exit %d stdout %q stderr %q", code, out, errOut)
	}
	if code, out, _ := runAt(t, "--json"); code != ExitOK || !strings.HasPrefix(out, `{"ok":true`) {
		t.Fatalf("atask --json: exit %d %s", code, out)
	}
	// JSON mode answers --help with an envelope.
	if code, out, _ := runAt(t, "--json", "--help"); code != ExitOK || !strings.HasPrefix(out, `{"ok":true`) {
		t.Fatalf("at --json --help: exit %d %s", code, out)
	}
}

func TestEveryCommandHonoursHelp(t *testing.T) {
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		code, out, errOut := runAt(t, n, "--help")
		if code != ExitOK || out == "" {
			t.Errorf("at %s --help: exit %d\nstdout:\n%s\nstderr:\n%s", n, code, out, errOut)
		}
	}
}

func TestVersionFromBuildInfo(t *testing.T) {
	info := func(version string, settings ...debug.BuildSetting) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Path: "github.com/zachbornheimer/ai-task", Version: version}, Settings: settings}, true
		}
	}
	none := func() (*debug.BuildInfo, bool) { return nil, false }
	cases := []struct {
		linked string
		read   func() (*debug.BuildInfo, bool)
		want   string
	}{
		{"v1.2.3", none, "v1.2.3"},
		{"dev", info("v0.0.0-20261010234435-bc3c473d2ffa"), "v0.0.0-20261010234435-bc3c473d2ffa"},
		{"dev", info("(devel)", debug.BuildSetting{Key: "vcs.revision", Value: "bc3c473d2ffa59a06fc49b1c252f9d3738b9eff6"}), "dev+bc3c473d2ffa"},
		{"dev", info("(devel)", debug.BuildSetting{Key: "vcs.revision", Value: "bc3c473d2ffa59a06fc49b1c252f9d3738b9eff6"}, debug.BuildSetting{Key: "vcs.modified", Value: "true"}), "dev+bc3c473d2ffa-dirty"},
		{"dev", info("(devel)"), "dev"},
		{"dev", none, "dev"},
	}
	for _, c := range cases {
		if got := buildVersion(c.linked, c.read); got != c.want {
			t.Errorf("buildVersion(%q) = %q, want %q", c.linked, got, c.want)
		}
	}
	// The command reports it, and honours --help.
	if code, out, _ := runAt(t, "version"); code != ExitOK || strings.TrimSpace(out) == "" {
		t.Fatalf("at version: exit %d %q", code, out)
	}
}
