package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestBareAtaskListsBoard(t *testing.T) {
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
	if code, _, errOut := run("add", "board item", "--check", "u: true"); code != ExitOK {
		t.Fatalf("add: %d %s", code, errOut)
	}
	code, out, errOut := run()
	if code != ExitOK {
		t.Fatalf("bare atask: %d %s", code, errOut)
	}
	if !strings.Contains(out, "board item") {
		t.Fatalf("bare atask should list the board:\n%s", out)
	}
	if strings.Contains(out, "Commands:") {
		t.Fatalf("bare atask must not print help:\n%s", out)
	}
}

func TestStatusWithoutProjectIsBoardWide(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	alpha := t.TempDir()
	other := t.TempDir()
	getenv := func(k string) string {
		if k == "ATASK_DB" {
			return db
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
	if code, _, errOut := run(alpha, "add", "alpha-work", "--check", "u: true"); code != ExitOK {
		t.Fatalf("add alpha: %d %s", code, errOut)
	}
	if code, _, errOut := run(other, "add", "loose-work", "--check", "u: true"); code != ExitOK {
		t.Fatalf("add unscoped: %d %s", code, errOut)
	}
	code, out, errOut := run(alpha, "status")
	if code != ExitOK {
		t.Fatalf("status: %d %s", code, errOut)
	}
	if strings.Contains(out+errOut, "NO_PROJECT") {
		t.Fatalf("status without --project must not require a project:\n%s %s", out, errOut)
	}
	if !strings.Contains(out, "2 tasks") && !strings.Contains(out, "2 open") {
		t.Fatalf("board-wide status from an enrolled repo should count every task:\n%s", out)
	}
}

func TestListTagsProjectOnMixedBoard(t *testing.T) {
	db := filepath.Join(t.TempDir(), "at.db")
	alpha := t.TempDir()
	other := t.TempDir()
	getenv := func(k string) string {
		if k == "ATASK_DB" {
			return db
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
	if code, _, errOut := run(alpha, "add", "named-work", "--check", "u: true"); code != ExitOK {
		t.Fatalf("add named: %d %s", code, errOut)
	}
	if code, _, errOut := run(other, "add", "loose-work", "--check", "u: true"); code != ExitOK {
		t.Fatalf("add loose: %d %s", code, errOut)
	}
	code, out, errOut := run(other, "list")
	if code != ExitOK {
		t.Fatalf("list: %d %s", code, errOut)
	}
	if !strings.Contains(out, "named-work") || !strings.Contains(out, "alpha") {
		t.Fatalf("named task should show the project:\n%s", out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "loose-work") && strings.Contains(line, "alpha") {
			t.Fatalf("unscoped row should stay untagged:\n%s", out)
		}
	}
}

func TestCommentIsLogNote(t *testing.T) {
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
		Args:   []string{"--json", "add", "claimed note", "--check", "u: true", "--claim"},
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
		OK     bool `json:"ok"`
		Result struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
			Session struct {
				Token string `json:"token"`
			} `json:"session"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil || env.Result.Session.Token == "" || env.Result.Task.ID == "" {
		t.Fatalf("claim token: %v %s", err, out.String())
	}
	token := env.Result.Session.Token
	id := env.Result.Task.ID
	withTok := func(k string) string {
		if k == "ATASK_SESSION" {
			return token
		}
		return getenv(k)
	}
	var cOut, cErr bytes.Buffer
	code = Main(Env{
		Args:   []string{"comment", "agents type this"},
		Stdin:  strings.NewReader(""),
		Stdout: &cOut,
		Stderr: &cErr,
		Cwd:    cwd,
		Getenv: withTok,
	})
	if code != ExitOK {
		t.Fatalf("comment: %d %s", code, cErr.String())
	}
	if !strings.Contains(cOut.String(), "Logged") {
		t.Fatalf("comment ack:\n%s", cOut.String())
	}
	var sOut, sErr bytes.Buffer
	code = Main(Env{
		Args:   []string{"show", id, "--full"},
		Stdin:  strings.NewReader(""),
		Stdout: &sOut,
		Stderr: &sErr,
		Cwd:    cwd,
		Getenv: getenv,
	})
	if code != ExitOK {
		t.Fatalf("show: %d %s", code, sErr.String())
	}
	if !strings.Contains(sOut.String(), "agents type this") {
		t.Fatalf("comment should land as a note:\n%s", sOut.String())
	}
}

func TestReadyListsClaimable(t *testing.T) {
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
	if code, _, errOut := run("add", "claimable work", "--check", "u: true"); code != ExitOK {
		t.Fatalf("add: %d %s", code, errOut)
	}
	code, out, errOut := run("ready")
	if code != ExitOK {
		t.Fatalf("ready: %d %s", code, errOut)
	}
	if !strings.Contains(out, "claimable work") {
		t.Fatalf("ready should list claimable tasks:\n%s", out)
	}
}
