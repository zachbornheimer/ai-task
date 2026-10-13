package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestResetAttemptsPlanner(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	cwd := t.TempDir()
	getenv := func(k string) string {
		if k == "ATASK_DB" {
			return db
		}
		return ""
	}
	run := func(extra map[string]string, args ...string) (int, string, string) {
		t.Helper()
		var out, errb bytes.Buffer
		code := Main(Env{
			Args:   args,
			Stdin:  strings.NewReader(""),
			Stdout: &out,
			Stderr: &errb,
			Cwd:    cwd,
			Getenv: func(k string) string {
				if extra != nil {
					if v, ok := extra[k]; ok {
						return v
					}
				}
				return getenv(k)
			},
		})
		return code, out.String(), errb.String()
	}

	code, out, errOut := run(nil, "--json", "add", "reset-budget", "--check", "u: true", "--claim")
	if code != ExitOK {
		t.Fatalf("add --claim: %d %s", code, errOut)
	}
	var claimed struct {
		Result struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
			Session struct {
				Token string `json:"token"`
			} `json:"session"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &claimed); err != nil || claimed.Result.Task.ID == "" || claimed.Result.Session.Token == "" {
		t.Fatalf("claim: %v %s", err, out)
	}
	id := claimed.Result.Task.ID
	token := claimed.Result.Session.Token

	code, out, errOut = run(map[string]string{"ATASK_SESSION": token}, "--json", "update", id, "--reset-attempts")
	if code == ExitOK {
		t.Fatalf("claimed worker --reset-attempts succeeded:\n%s", out)
	}
	if !strings.Contains(out, "PLAN_CONFLICT") {
		t.Fatalf("worker reset want PLAN_CONFLICT, got %d stdout=%s stderr=%s", code, out, errOut)
	}

	code, out, errOut = run(nil, "--json", "update", id, "--reset-attempts", "--planner")
	if code != ExitOK {
		t.Fatalf("planner --reset-attempts: %d %s %s", code, out, errOut)
	}

	code, out, errOut = run(nil, "--json", "show", id, "--full")
	if code != ExitOK {
		t.Fatalf("show: %d %s", code, errOut)
	}
	var shown struct {
		Result struct {
			Handoff *struct {
				RecentLogs []struct {
					Note string `json:"note"`
				} `json:"recent_logs"`
			} `json:"handoff"`
			History *struct {
				Logs []struct {
					Note string `json:"note"`
				} `json:"logs"`
			} `json:"history"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatalf("show json: %v %s", err, out)
	}
	var notes []string
	if shown.Result.Handoff != nil {
		for _, l := range shown.Result.Handoff.RecentLogs {
			notes = append(notes, l.Note)
		}
	}
	if shown.Result.History != nil {
		for _, l := range shown.Result.History.Logs {
			notes = append(notes, l.Note)
		}
	}
	found := false
	for _, n := range notes {
		low := strings.ToLower(n)
		if strings.Contains(low, "planner") && strings.Contains(low, "reset") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("planner reset not in history notes=%q\n%s", notes, out)
	}
}
