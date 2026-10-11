package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestHookSessionStartPrintsBoard(t *testing.T) {
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
	if code, _, errOut := run("", "add", "prime me", "--check", "u: true"); code != ExitOK {
		t.Fatalf("add: %d %s", code, errOut)
	}
	code, out, errOut := run(`{"hook_event_name":"SessionStart","cwd":"`+cwd+`"}`, "hook", "session-start")
	if code != ExitOK {
		t.Fatalf("session-start: %d %s", code, errOut)
	}
	got := sessionStartContext(t, out)
	if !strings.Contains(got, "prime me") {
		t.Fatalf("session-start additionalContext should list the board:\n%s", got)
	}
}

func TestHookSessionStartPrintsWhoamiWhenClaimed(t *testing.T) {
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
		Args:   []string{"--json", "add", "held work", "--check", "u: true", "--claim"},
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
	if !strings.Contains(got, "# Task "+env.Result.Task.ID) {
		t.Fatalf("claimed session-start additionalContext should whoami:\n%s", got)
	}
	if !strings.Contains(got, "held work") {
		t.Fatalf("claimed session-start additionalContext should still list the board:\n%s", got)
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
