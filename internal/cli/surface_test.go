package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentContractWordCount(t *testing.T) {
	n := agentContractWordCount()
	if n < 300 || n > 500 {
		t.Fatalf("agent contract is %d words; want 300-500", n)
	}
}

func TestContextNeedsNoDatabase(t *testing.T) {
	var out, errb bytes.Buffer
	code := Main(Env{
		Args:   []string{"context"},
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errb,
		Cwd:    t.TempDir(),
		Getenv: func(k string) string {
			if k == "ATASK_DB" {
				return filepath.Join(t.TempDir(), "missing", "no.db")
			}
			return ""
		},
	})
	if code != ExitOK {
		t.Fatalf("context without a db: %d %s", code, errb.String())
	}
	got := out.String()
	for _, want := range []string{
		"verifiable objectives",
		"atask context ID",
		"atask list",
		"atask show",
		"atask claim",
		"atask log",
		"verify complete",
		"lease",
		"claim release",
		"Do not assume complete",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("context lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Claimable now") || strings.Contains(got, "# Board") || strings.Contains(got, "# Task ") {
		t.Fatalf("context must not dump the board or a task:\n%s", got)
	}
}

func TestContextRejectsTwoArgs(t *testing.T) {
	var out, errb bytes.Buffer
	code := Main(Env{
		Args:   []string{"context", "at-abc123", "at-def456"},
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errb,
		Cwd:    t.TempDir(),
		Getenv: func(string) string { return "" },
	})
	if code == ExitOK {
		t.Fatal("context with two ids must be usage")
	}
	if !strings.Contains(errb.String(), "no arguments, or one task id") {
		t.Fatalf("context extra args:\n%s", errb.String())
	}
}

func TestContextHandoffForTaskID(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	cwd := t.TempDir()
	getenv := func(k string) string {
		if k == "ATASK_DB" {
			return db
		}
		return ""
	}
	var out, errb bytes.Buffer
	code := Main(Env{
		Args:   []string{"--json", "add", "handoff-work", "--proof", "run: test -d .", "--regression", "run: test -d ."},
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errb,
		Cwd:    cwd,
		Getenv: getenv,
	})
	if code != ExitOK {
		t.Fatalf("add: %d %s", code, errb.String())
	}
	var env struct {
		Result struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil || env.Result.Task.ID == "" {
		t.Fatalf("add json: %v %s", err, out.String())
	}
	var cOut, cErr bytes.Buffer
	code = Main(Env{
		Args:   []string{"context", env.Result.Task.ID},
		Stdin:  strings.NewReader(""),
		Stdout: &cOut,
		Stderr: &cErr,
		Cwd:    cwd,
		Getenv: getenv,
	})
	if code != ExitOK {
		t.Fatalf("context id: %d %s", code, cErr.String())
	}
	got := cOut.String()
	if !strings.Contains(got, "# Task "+env.Result.Task.ID) || !strings.Contains(got, "handoff-work") {
		t.Fatalf("handoff should name the task:\n%s", got)
	}
	if strings.Contains(got, "atask tracks verifiable objectives") {
		t.Fatalf("task handoff must not reprint the operating contract:\n%s", got)
	}
	if strings.Contains(got, "Claimable now") || strings.Contains(got, "# Board") {
		t.Fatalf("task handoff must not dump the board:\n%s", got)
	}
}

func TestContextCompletedTaskIssuesNoWork(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	cwd := t.TempDir()
	getenv := func(k string) string {
		if k == "ATASK_DB" {
			return db
		}
		return ""
	}
	var out, errb bytes.Buffer
	code := Main(Env{
		Args:   []string{"--json", "add", "already-done", "--proof", "run: test -d .", "--regression", "run: test -d .", "--claim"},
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errb,
		Cwd:    cwd,
		Getenv: getenv,
	})
	if code != ExitOK {
		t.Fatalf("add --claim: %d %s", code, errb.String())
	}
	var env struct {
		Result struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
			Session struct {
				Token string `json:"token"`
			} `json:"session"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil || env.Result.Session.Token == "" {
		t.Fatalf("claim: %v %s", err, out.String())
	}
	getenvTok := func(k string) string {
		if k == "ATASK_SESSION" {
			return env.Result.Session.Token
		}
		return getenv(k)
	}
	var vOut, vErr bytes.Buffer
	if code := Main(Env{Args: []string{"verify", "complete"}, Stdin: strings.NewReader(""), Stdout: &vOut, Stderr: &vErr, Cwd: cwd, Getenv: getenvTok}); code != ExitOK {
		t.Fatalf("verify complete: %d %s %s", code, vOut.String(), vErr.String())
	}
	var cOut, cErr bytes.Buffer
	code = Main(Env{
		Args:   []string{"context", env.Result.Task.ID},
		Stdin:  strings.NewReader(""),
		Stdout: &cOut,
		Stderr: &cErr,
		Cwd:    cwd,
		Getenv: getenv,
	})
	if code != ExitOK {
		t.Fatalf("context complete: %d %s", code, cErr.String())
	}
	got := cOut.String()
	if !strings.Contains(got, "This task is complete") {
		t.Fatalf("completed handoff:\n%s", got)
	}
	for _, banned := range []string{
		"Nobody holds this task",
		"atask claim " + env.Result.Task.ID,
		"Done when `atask verify complete`",
		"Commit with",
	} {
		if strings.Contains(got, banned) {
			t.Fatalf("completed handoff must not instruct more work (%q):\n%s", banned, got)
		}
	}
}

func TestWhoamiIsNotACommand(t *testing.T) {
	var out, errb bytes.Buffer
	code := Main(Env{
		Args:   []string{"whoami"},
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errb,
		Cwd:    t.TempDir(),
		Getenv: func(string) string { return "" },
	})
	if code == ExitOK {
		t.Fatal("whoami must not be a command")
	}
	if !strings.Contains(errb.String(), `unknown command "whoami"`) {
		t.Fatalf("whoami unknown:\n%s", errb.String())
	}
}

func TestHelpIsTheCommandReference(t *testing.T) {
	var out, errb bytes.Buffer
	code := Main(Env{
		Args:   []string{"help"},
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errb,
		Cwd:    t.TempDir(),
		Getenv: func(string) string { return "" },
	})
	if code != ExitOK {
		t.Fatalf("help: %d %s", code, errb.String())
	}
	got := out.String()
	for _, want := range []string{
		"atask context",
		"atask context ID",
		"atask help",
		"atask version",
		"atask add",
		"atask update",
		"atask list",
		"atask show",
		"atask claim",
		"atask log",
		"atask verify",
		"ready",
		"blocked",
		"complete",
		"archived",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("help lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "whoami") {
		t.Fatalf("help must not list whoami:\n%s", got)
	}
}

func TestShowWithoutClaimNeedsAnID(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	var out, errb bytes.Buffer
	code := Main(Env{
		Args:   []string{"show"},
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errb,
		Cwd:    t.TempDir(),
		Getenv: func(k string) string {
			if k == "ATASK_DB" {
				return db
			}
			return ""
		},
	})
	if code == ExitOK {
		t.Fatal("show with no id and no claim must fail")
	}
	if !strings.Contains(errb.String(), "task ID is required") {
		t.Fatalf("show no-id error:\n%s", errb.String())
	}
}

func TestShowNoArgsIsTheCurrentClaim(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	cwd := t.TempDir()
	getenv := func(k string) string {
		if k == "ATASK_DB" {
			return db
		}
		return ""
	}
	var out, errb bytes.Buffer
	code := Main(Env{
		Args:   []string{"--json", "add", "held surface", "--proof", "run: test -d .", "--regression", "run: test -d .", "--claim"},
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errb,
		Cwd:    cwd,
		Getenv: getenv,
	})
	if code != ExitOK {
		t.Fatalf("add --claim: %d %s", code, errb.String())
	}
	var env struct {
		Result struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
			Session struct {
				Token string `json:"token"`
			} `json:"session"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil || env.Result.Session.Token == "" {
		t.Fatalf("claim: %v %s", err, out.String())
	}
	var sOut, sErr bytes.Buffer
	code = Main(Env{
		Args:   []string{"show"},
		Stdin:  strings.NewReader(""),
		Stdout: &sOut,
		Stderr: &sErr,
		Cwd:    cwd,
		Getenv: func(k string) string {
			if k == "ATASK_SESSION" {
				return env.Result.Session.Token
			}
			return getenv(k)
		},
	})
	if code != ExitOK {
		t.Fatalf("show current: %d %s", code, sErr.String())
	}
	if !strings.Contains(sOut.String(), env.Result.Task.ID) || !strings.Contains(sOut.String(), "held surface") {
		t.Fatalf("show current should be the claimed task:\n%s", sOut.String())
	}
}

func TestListDefaultIncludesComplete(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	cwd := t.TempDir()
	getenv := func(k string) string {
		if k == "ATASK_DB" {
			return db
		}
		return ""
	}
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := Main(Env{Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Cwd: cwd, Getenv: getenv})
		return code, out.String(), errb.String()
	}
	code, out, errOut := run("--json", "add", "finished-work", "--proof", "run: test -d .", "--regression", "run: test -d .", "--claim")
	if code != ExitOK {
		t.Fatalf("add --claim: %d %s", code, errOut)
	}
	var env struct {
		Result struct {
			Session struct {
				Token string `json:"token"`
			} `json:"session"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("claim json: %v %s", err, out)
	}
	getenvTok := func(k string) string {
		if k == "ATASK_SESSION" {
			return env.Result.Session.Token
		}
		return getenv(k)
	}
	var vOut, vErr bytes.Buffer
	if code := Main(Env{Args: []string{"verify", "complete"}, Stdin: strings.NewReader(""), Stdout: &vOut, Stderr: &vErr, Cwd: cwd, Getenv: getenvTok}); code != ExitOK {
		t.Fatalf("verify complete: %d %s %s", code, vOut.String(), vErr.String())
	}
	code, out, errOut = run("list")
	if code != ExitOK {
		t.Fatalf("list: %d %s", code, errOut)
	}
	if !strings.Contains(out, "finished-work") {
		t.Fatalf("default list must include completed tasks:\n%s", out)
	}
}

func TestListColorsGlyphsAndDimsProject(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	alpha := t.TempDir()
	getenv := func(k string) string {
		switch k {
		case "ATASK_DB":
			return db
		case "ATASK_COLOR":
			return "always"
		}
		return ""
	}
	run := func(cwd string, args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := Main(Env{Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Cwd: cwd, Getenv: getenv})
		return code, out.String(), errb.String()
	}
	if code, _, errOut := run(alpha, "init", "--name", "alpha", "--no-instructions"); code != ExitOK {
		t.Fatalf("init: %d %s", code, errOut)
	}
	if code, _, errOut := run(alpha, "add", "colored-work", "--proof", "run: test -d .", "--regression", "run: test -d ."); code != ExitOK {
		t.Fatalf("add: %d %s", code, errOut)
	}
	loose := t.TempDir()
	if code, _, errOut := run(loose, "add", "claimed-work", "--proof", "run: test -d .", "--regression", "run: test -d .", "--claim"); code != ExitOK {
		t.Fatalf("claim add: %d %s", code, errOut)
	}
	code, out, errOut := run(alpha, "list")
	if code != ExitOK {
		t.Fatalf("list: %d %s", code, errOut)
	}
	if !strings.Contains(out, "[project: alpha]") {
		t.Fatalf("project tag:\n%s", out)
	}
	if !strings.Contains(out, "\x1b[2m  [project: alpha]\x1b[22m") {
		t.Fatalf("project tag should be dim:\n%q", out)
	}
	if !strings.Contains(out, "\x1b[34m◐\x1b[39m") {
		t.Fatalf("in-progress glyph should be blue:\n%q", out)
	}
	if strings.Contains(out, "\x1b[97m") || strings.Contains(out, "\x1b[37m") {
		t.Fatalf("must not use bright/white on titles:\n%q", out)
	}
}

func TestNOCOLORDisablesColor(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	cwd := t.TempDir()
	getenv := func(k string) string {
		switch k {
		case "ATASK_DB":
			return db
		case "ATASK_COLOR":
			return "always"
		case "NO_COLOR":
			return "1"
		}
		return ""
	}
	run := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := Main(Env{Args: args, Stdin: strings.NewReader(""), Stdout: &o, Stderr: &e, Cwd: cwd, Getenv: getenv})
		return code, o.String(), e.String()
	}
	if code, _, errOut := run("add", "plain-work", "--proof", "run: test -d .", "--regression", "run: test -d ."); code != ExitOK {
		t.Fatalf("add: %d %s", code, errOut)
	}
	code, out, errOut := run("list")
	if code != ExitOK {
		t.Fatalf("list: %d %s", code, errOut)
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("NO_COLOR must suppress ANSI:\n%q", out)
	}
}
