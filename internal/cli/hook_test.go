package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestHookSessionStartSilentWithoutClaim(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	cwd := t.TempDir()
	getenv := func(k string) string {
		if k == "ATASK_DB" {
			return db
		}
		return ""
	}
	run := func(stdin string, args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := Main(Env{Args: args, Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb, Cwd: cwd, Getenv: getenv})
		return code, out.String(), errb.String()
	}
	if code, _, errOut := run("", "add", "prime me", "--check", "u: true", "--regression-check", "r: true"); code != ExitOK {
		t.Fatalf("add: %d %s", code, errOut)
	}
	code, out, errOut := run(`{"hook_event_name":"SessionStart","cwd":"`+cwd+`"}`, "hook", "session-start")
	if code != ExitOK {
		t.Fatalf("session-start: %d %s", code, errOut)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("session-start without a claim must inject nothing:\n%s", out)
	}
}

func TestHookSessionStartCurrentClaimOnly(t *testing.T) {
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
		Args:   []string{"--json", "add", "held work", "--check", "u: true", "--regression-check", "r: true", "--claim"},
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errb,
		Cwd:    cwd,
		Getenv: getenv,
	})
	if code != ExitOK {
		t.Fatalf("add --claim: %d %s", code, errb.String())
	}
	var other bytes.Buffer
	if code := Main(Env{
		Args:   []string{"add", "other work", "--check", "u: true", "--regression-check", "r: true"},
		Stdin:  strings.NewReader(""),
		Stdout: &other,
		Stderr: new(bytes.Buffer),
		Cwd:    cwd,
		Getenv: getenv,
	}); code != ExitOK {
		t.Fatalf("add other: %d %s", code, other.String())
	}
	var env struct {
		Result struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
			Session struct {
				Token     string `json:"token"`
				Workspace string `json:"workspace"`
			} `json:"session"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil || env.Result.Session.Token == "" {
		t.Fatalf("claim: %v %s", err, out.String())
	}
	ws := env.Result.Session.Workspace
	if ws == "" {
		ws = cwd
	}
	withTok := func(k string) string {
		if k == "ATASK_SESSION" {
			return env.Result.Session.Token
		}
		return getenv(k)
	}
	var hOut, hErr bytes.Buffer
	code = Main(Env{
		Args:   []string{"hook", "session-start"},
		Stdin:  strings.NewReader(`{"hook_event_name":"SessionStart","cwd":"` + ws + `"}`),
		Stdout: &hOut,
		Stderr: &hErr,
		Cwd:    ws,
		Getenv: withTok,
	})
	if code != ExitOK {
		t.Fatalf("session-start claimed: %d %s", code, hErr.String())
	}
	got := sessionStartContext(t, hOut.String())
	if !strings.Contains(got, "# Task "+env.Result.Task.ID) || !strings.Contains(got, "held work") {
		t.Fatalf("claimed session-start should name the current task:\n%s", got)
	}
	if strings.Contains(got, "other work") {
		t.Fatalf("session-start must not dump the board:\n%s", got)
	}
}

func TestHookStopDoesNotBlockOpenClaim(t *testing.T) {
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
		Args:   []string{"--json", "add", "live claim", "--check", "u: true", "--regression-check", "r: true", "--claim"},
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
			Session struct {
				Token     string `json:"token"`
				Workspace string `json:"workspace"`
			} `json:"session"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil || env.Result.Session.Token == "" {
		t.Fatalf("claim: %v %s", err, out.String())
	}
	ws := env.Result.Session.Workspace
	if ws == "" {
		ws = cwd
	}
	var sOut, sErr bytes.Buffer
	code = Main(Env{
		Args:   []string{"hook", "stop"},
		Stdin:  strings.NewReader(`{"hook_event_name":"Stop","stop_hook_active":false,"cwd":"` + ws + `"}`),
		Stdout: &sOut,
		Stderr: &sErr,
		Cwd:    ws,
		Getenv: func(k string) string {
			if k == "ATASK_SESSION" {
				return env.Result.Session.Token
			}
			return getenv(k)
		},
	})
	if code != ExitOK {
		t.Fatalf("stop: %d %s", code, sErr.String())
	}
	if strings.TrimSpace(sOut.String()) != "" {
		t.Fatalf("stop must not block an open claim:\n%s", sOut.String())
	}
}

func sessionStartContext(t *testing.T, out string) string {
	t.Helper()
	var env struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("session-start stdout must be hookSpecificOutput JSON: %v\n%s", err, out)
	}
	if env.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Fatalf("hookEventName=%q", env.HookSpecificOutput.HookEventName)
	}
	if env.HookSpecificOutput.AdditionalContext == "" {
		t.Fatal("additionalContext empty")
	}
	return env.HookSpecificOutput.AdditionalContext
}
