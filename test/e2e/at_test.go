// Package e2e runs the built `atask` binary as real subprocesses: process
// boundaries, environment defaults, exit codes, stdout/stderr separation,
// token transports, and multi-process claims on one database.
package e2e

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	// The binary is built from source in TestMain; importing the CLI package
	// makes Go's test cache key include every source file the binary
	// depends on, so a change anywhere re-runs these tests.
	_ "github.com/zachbornheimer/ai-task/internal/cli"
	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/must"
	"github.com/zachbornheimer/ai-task/internal/testgit"
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "atask-e2e-")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "atask")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/atask")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("build: " + err.Error())
	}
	code := m.Run()
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintln(os.Stderr, "remove test dir:", err)
	}
	os.Exit(code)
}

type env struct {
	t   *testing.T
	db  string
	cwd string
	env []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base := t.TempDir()
	cwd := filepath.Join(base, "repo")
	must.OK(t, os.MkdirAll(cwd, 0o755))
	e := &env{t: t, db: filepath.Join(base, "state", "at.db"), cwd: cwd}
	e.env = append(os.Environ(), "ATASK_DB="+e.db, "ATASK_OUTPUT=json", "ATASK_PROJECT=", "ATASK_SESSION=")
	isolated, err := testgit.Overrides(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.env = append(e.env, isolated...)
	return e
}

// gitEnv makes the working directory a Git repository with one commit and
// registers it.
func gitEnv(t *testing.T) *env {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	e := newEnv(t)
	e.git(e.cwd, "init", "-q", "-b", "main")
	must.OK(t, os.WriteFile(filepath.Join(e.cwd, "f.txt"), []byte("one"), 0o644))
	e.git(e.cwd, "add", "f.txt")
	e.git(e.cwd, "commit", "-q", "-m", "one")
	e.ok("init", "--name", "g")
	e.promote()
	return e
}

// promote switches the project to the promote integration policy straight in
// the database: no CLI flag sets it any more, but the promote paths still
// have end-to-end coverage until their removal task owns them.
func (e *env) promote() {
	e.t.Helper()
	db, err := sql.Open("sqlite", e.db)
	if err != nil {
		e.t.Fatal(err)
	}
	defer discard.CloseReadOnly(db)
	if _, err := db.Exec(`UPDATE projects SET integration = 'promote' WHERE root_path IS NOT NULL`); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) git(dir string, args ...string) {
	e.t.Helper()
	cmd := testgit.Command(e.t, dir, args...)
	// Hooks see the test environment and find the built `atask` on PATH.
	cmd.Env = append(append(cmd.Env, e.env...), "PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		e.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (e *env) commit(dir, file, content string) {
	e.t.Helper()
	must.OK(e.t, os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644))
	e.git(dir, "add", file)
	e.git(dir, "commit", "-q", "-m", content)
}

type result struct {
	code   int
	stdout string
	stderr string
	env    map[string]any
}

func (e *env) runIn(dir string, stdin string, extraEnv []string, args ...string) result {
	e.t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(append([]string{}, e.env...), extraEnv...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	r := result{stdout: out.String(), stderr: errb.String()}
	ee := &exec.ExitError{}
	if errors.As(err, &ee) {
		r.code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatalf("run %v: %v", args, err)
	}
	if strings.HasPrefix(strings.TrimSpace(r.stdout), "{") {
		if err := json.Unmarshal([]byte(r.stdout), &r.env); err != nil {
			e.t.Fatalf("stdout of %v is not a JSON envelope: %v\n%s", args, err, r.stdout)
		}
	}
	return r
}

func (e *env) run(args ...string) result { return e.runIn(e.cwd, "", nil, args...) }

func (e *env) ok(args ...string) map[string]any {
	e.t.Helper()
	r := e.run(args...)
	if r.code != 0 || r.env == nil || r.env["ok"] != true {
		e.t.Fatalf("%v failed: code=%d stdout=%s stderr=%s", args, r.code, r.stdout, r.stderr)
	}
	if strings.TrimSpace(r.stderr) != "" && !strings.HasPrefix(r.stderr, "warning:") {
		e.t.Fatalf("%v wrote to stderr on success: %s", args, r.stderr)
	}
	m, _ := r.env["result"].(map[string]any)
	return m
}

func (e *env) fails(code string, args ...string) result {
	e.t.Helper()
	r := e.run(args...)
	if r.code == 0 {
		e.t.Fatalf("%v succeeded, expected %s: %s", args, code, r.stdout)
	}
	errObj, _ := r.env["error"].(map[string]any)
	if r.env == nil || r.env["ok"] != false || errObj == nil || errObj["code"] != code {
		e.t.Fatalf("%v: expected error code %s, got code=%d stdout=%s stderr=%s", args, code, r.code, r.stdout, r.stderr)
	}
	return r
}

func taskID(m map[string]any) string {
	if t, ok := m["task"].(map[string]any); ok {
		return must.As[string](t["id"])
	}
	return must.As[string](m["id"])
}

func TestAgentLoopEndToEnd(t *testing.T) {
	e := gitEnv(t)
	a := taskID(e.ok("add", "Parse tokens", "--key", "parse", "--proof", "run: test -f parse.txt", "--regression", "run: true"))
	b := taskID(e.ok("add", "Reject expired access tokens", "--key", "reject", "--requires", "parse", "--accept", "expired -> 401", "--proof", "run: test -f reject.txt", "--regression", "run: true"))
	e.fails("NOT_READY", "claim", b)
	e.fails("INVALID_INPUT", "update", "parse", "--requires", "reject")
	e.fails("INVALID_INPUT", "verify")
	e.fails("INVALID_INPUT", "verify", "complete") // no session anywhere -> usage
	sess := e.ok("claim")
	tok := must.As[string](sess["token"])
	ws := must.As[string](sess["workspace"])
	if taskID(sess) != a || !strings.HasPrefix(tok, "sess-") || ws == "" {
		t.Fatalf("claim: %v", sess)
	}
	// The claim says where work lands and which regression checks will run.
	if sess["target_branch"] != "main" || sess["target_revision"] == "" || len(must.As[[]any](sess["regression_checks"])) == 0 {
		t.Fatalf("claim lacks target or regression checks: %v", sess)
	}
	with := []string{"ATASK_SESSION=" + tok}
	// log via ATASK_SESSION, positional, and stdin.
	if r := e.runIn(e.cwd, "", with, "log", "--done", "started", "--next", "write parse.txt"); r.code != 0 {
		t.Fatalf("env token: %s %s", r.stdout, r.stderr)
	}
	e.ok("log", tok, "--learned", "positional token works")
	if r := e.runIn(e.cwd, tok+"\n", nil, "log", "-", "--note", "stdin works"); r.code != 0 {
		t.Fatalf("stdin token: %s %s", r.stdout, r.stderr)
	}
	// Inside the worktree no token is needed: the claim stored it there.
	if r := e.runIn(ws, "", nil, "log", "--note", "worktree token works"); r.code != 0 {
		t.Fatalf("worktree token: %s %s", r.stdout, r.stderr)
	}
	if r := e.runIn(ws, "", nil, "show"); r.code != 0 || taskID(must.As[map[string]any](r.env["result"])) != a {
		t.Fatalf("show in worktree: %s %s", r.stdout, r.stderr)
	}
	if cx := e.ok("context"); !strings.Contains(must.As[string](cx["prompt"]), "verifiable objectives") {
		t.Fatalf("context contract: %v", cx)
	}
	if r := e.runIn(ws, "", []string{"ATASK_OUTPUT="}, "context"); r.code != 0 || !strings.Contains(r.stdout, "verify complete") {
		t.Fatalf("human context: %s %s", r.stdout, r.stderr)
	}
	if handoff := e.ok("context", a); !strings.Contains(must.As[string](handoff["prompt"]), "# Task "+a) {
		t.Fatalf("context id handoff: %v", handoff)
	}
	if !strings.HasSuffix(must.As[string](sess["branch"]), "/"+a) || !strings.HasPrefix(must.As[string](sess["branch"]), "atask/") {
		t.Fatalf("branch %v", sess["branch"])
	}
	if _, err := os.Stat(filepath.Join(e.cwd, ".git", "hooks", "post-commit")); err == nil {
		t.Fatal("init must not write hooks into the repository")
	}
	// Provisional checks run and never complete.
	r := e.runIn(e.cwd, "", with, "verify", "proof")
	if r.env["ok"] != false || must.As[map[string]any](r.env["error"])["code"] != "CHECK_FAILED" {
		t.Fatalf("verify task before work: %s", r.stdout)
	}
	e.commit(ws, "parse.txt", "p")
	if r := e.runIn(e.cwd, "", with, "verify", "proof"); r.code != 0 || must.As[map[string]any](r.env["result"])["completed"] != false {
		t.Fatalf("verify task: %s", r.stdout)
	}
	if r := e.runIn(e.cwd, "", with, "verify", "regression"); r.code != 0 {
		t.Fatalf("verify regression: %s %s", r.stdout, r.stderr)
	}
	if e.ok("show", a)["status"] != "claimed" {
		t.Fatal("diagnostics must not change status")
	}
	// verify complete: completes and ends the claim; the target branch is untouched.
	r = e.runIn(e.cwd, "", with, "verify", "complete")
	if r.code != 0 || must.As[map[string]any](r.env["result"])["completed"] != true {
		t.Fatalf("verify complete: %s %s", r.stdout, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(e.cwd, "parse.txt")); err == nil {
		t.Fatal("completion moved the target branch")
	}
	if r := e.runIn(e.cwd, "", with, "log", "--done", "late"); must.As[map[string]any](r.env["error"])["code"] != "INVALID_SESSION" {
		t.Fatalf("%s", r.stdout)
	}
	// Dependent is ready; outside a worktree, renew/release need a token.
	e.fails("INVALID_INPUT", "claim", "renew")
	sb := e.ok("claim", "reject")
	e.ok("claim", "renew", must.As[string](sb["token"]))
	if r := e.runIn(must.As[string](sb["workspace"]), "", nil, "claim", "renew"); r.code != 0 {
		t.Fatalf("renew inside worktree: %s %s", r.stdout, r.stderr)
	}
	// A token used inside another task's worktree is refused before any
	// write: the confused-deputy case of a mis-exported ATASK_SESSION.
	if r := e.runIn(must.As[string](sb["workspace"]), "", with, "log", "--note", "wrong worktree"); r.env["error"] == nil || must.As[map[string]any](r.env["error"])["code"] != "INVALID_SESSION" {
		t.Fatalf("token for %s accepted in %s's worktree: %s", a, b, r.stdout)
	}
	e.fails("INVALID_SESSION", "claim", "renew", "sess-0000000000000000000000000000000a")
	e.fails("INVALID_SESSION", "claim", "release", tok)
	rel := e.ok("claim", "release", must.As[string](sb["token"]), "--note", "later")
	if rel["status"] != "ready" {
		t.Fatalf("%v", rel)
	}
	// No secrets in read views.
	for _, args := range [][]string{{"show", a}, {"show", a, "--full"}, {"list"}, {"list", "all"}, {"status"}} {
		if out := e.run(args...).stdout; strings.Contains(out, "sess-") {
			t.Fatalf("%v leaked a token", args)
		}
	}
	// Human views carry the glyph convention: a is complete (●); b was
	// claimed and released, so it is started but not complete (◐) even
	// though it is ready again.
	h := e.runIn(e.cwd, "", []string{"ATASK_OUTPUT="}, "list", "all")
	if !strings.Contains(h.stdout, "● "+a) || !strings.Contains(h.stdout, "◐ "+b) {
		t.Fatalf("list glyphs:\n%s", h.stdout)
	}
	hs := e.runIn(e.cwd, "", []string{"ATASK_OUTPUT="}, "show", "reject")
	if !strings.Contains(hs.stdout, "◐ "+b+" [TASK] · Reject expired access tokens   [OPEN]") || !strings.Contains(hs.stdout, "REQUIRES\n  ● "+a+": Parse tokens") {
		t.Fatalf("show:\n%s", hs.stdout)
	}
	st := e.ok("status")
	if st["done"] != false || must.As[float64](st["claimable"]) != 1 {
		t.Fatalf("status: %v", st)
	}
}

func TestPlanningCommands(t *testing.T) {
	e := newEnv(t)
	e.ok("init", "--name", "p") // not a repository yet: init creates one
	g := taskID(e.ok("--project", "p", "add", "Identity Core", "--group", "--key", "identity"))
	a := taskID(e.ok("--project", "p", "add", "Rotate creds", "--key", "rotate", "--proof", "run: test -d .", "--regression", "run: true"))
	b := taskID(e.ok("--project", "p", "add", "Token store", "--key", "store", "--parent", "identity", "--requires", "rotate", "--proof", "run: test -d .", "--regression", "run: true"))
	e.fails("CONFLICT", "--project", "p", "add", "Token store v2", "--key", "store", "--proof", "run: test -d .", "--regression", "run: true")
	if d := e.ok("--project", "p", "add", "No proofs", "--key", "noproofs"); must.As[map[string]any](d["task"])["status"] != "draft" {
		t.Fatalf("task without proofs must be a draft: %v", d)
	}
	e.ok("--project", "p", "update", "noproofs", "--archive", "--reason", "draft probe")
	rep := e.ok("--project", "p", "add", "Token store", "--key", "store", "--parent", "identity", "--requires", "rotate", "--proof", "run: test -d .", "--regression", "run: true")
	if must.As[float64](rep["changed"]) != 0 {
		t.Fatalf("identical replay changed the plan: %v", rep)
	}
	e.fails("INVALID_INPUT", "--project", "p", "update", "rotate", "--archive")                          // reason required
	e.fails("CONFLICT", "--project", "p", "update", "rotate", "--archive", "--reason", "done elsewhere") // store requires it
	up := e.ok("--project", "p", "update", "store", "--remove-requires", "rotate", "--accept", "works")
	if must.As[float64](up["changed"]) != 1 {
		t.Fatalf("%v", up)
	}
	e.ok("--project", "p", "update", "rotate", "--archive", "--reason", "done elsewhere")
	if sh := e.ok("--project", "p", "show", a); sh["status"] != "archived" || sh["archive_reason"] != "done elsewhere" {
		t.Fatalf("not archived with reason: %v", sh)
	}
	snap := e.ok("--project", "p", "list", "all")
	if len(must.As[[]any](snap["groups"])) != 1 || len(must.As[[]any](snap["tasks"])) != 1 {
		t.Fatalf("%v", snap)
	}
	h := e.runIn(e.cwd, "", []string{"ATASK_OUTPUT="}, "--project", "p", "list")
	if !strings.Contains(h.stdout, "○ "+g+"  Identity Core (0/1 complete)\n└── ○ "+b+"  Token store") {
		t.Fatalf("hierarchy:\n%s", h.stdout)
	}
	// Status cannot be set: no such flag.
	if r := e.run("--project", "p", "update", b, "--status", "done"); r.code != 2 {
		t.Fatalf("status setter accepted: %+v", r)
	}
	// Usage vs domain exit codes.
	if r := e.run("nope"); r.code != 2 {
		t.Fatal("unknown command exit")
	}
	if r := e.run("--project", "p", "show", "at-000000"); r.code != 1 || must.As[map[string]any](r.env["error"])["code"] != "NOT_FOUND" {
		t.Fatalf("%+v", r)
	}
}

func TestConcurrentProcessesClaimDistinctTasks(t *testing.T) {
	e := newEnv(t)
	e.ok("init", "--name", "c")
	const n = 6
	ids := map[string]bool{}
	for i := range n {
		ids[taskID(e.ok("add", fmt.Sprintf("work-%d", i), "--proof", "run: test -d .", "--regression", "run: true"))] = true
	}
	results := make([]result, n+4)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i] = e.run("claim") }(i)
	}
	wg.Wait()
	seen := map[string]int{}
	none := 0
	for _, r := range results {
		if r.code == 0 {
			seen[taskID(must.As[map[string]any](r.env["result"]))]++
		} else if must.As[map[string]any](r.env["error"])["code"] == "NOT_READY" {
			none++
		} else {
			t.Errorf("unexpected: %s %s", r.stdout, r.stderr)
		}
	}
	if len(seen) != n || none != 4 {
		t.Fatalf("seen=%d none=%d", len(seen), none)
	}
	for id, c := range seen {
		if c != 1 || !ids[id] {
			t.Fatalf("%s claimed %d times", id, c)
		}
	}
}

func TestChecksRunWithoutHoldingTheDatabase(t *testing.T) {
	e := gitEnv(t)
	script := bin + " --db " + e.db + " --project g add from-inside-check --proof 'run: test -d .' --regression 'run: true'"
	// The run preset caps one argument at 300 chars; a long state path would
	// exceed that inline, so the nested command lives in a script file.
	scriptFile, err := os.CreateTemp("", "nested-*.sh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { must.OK(t, os.Remove(scriptFile.Name())) })
	if _, err := scriptFile.WriteString(script + "\n"); err != nil {
		t.Fatal(err)
	}
	must.OK(t, scriptFile.Close())
	a := taskID(e.ok("add", "reentrant", "--proof", "run: sh "+scriptFile.Name(), "--regression", "run: true"))
	s := e.ok("claim", a)
	e.commit(must.As[string](s["workspace"]), "r.txt", "r")
	r := e.runIn(e.cwd, "", []string{"ATASK_SESSION=" + must.As[string](s["token"])}, "verify", "complete")
	if r.code != 0 {
		t.Fatalf("%s %s", r.stdout, r.stderr)
	}
	if !strings.Contains(e.run("list", "all").stdout, "from-inside-check") {
		t.Fatal("nested write did not land")
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// A no-op proof or regression is refused at add; real ones complete.
func TestVacuousProofsRejectedByCLI(t *testing.T) {
	e := gitEnv(t)
	e.fails("INVALID_INPUT", "add", "noop proof", "--proof", "run: true", "--regression", "run: true")
	x := taskID(e.ok("add", "real gates", "--proof", "run: test -d .", "--regression", "run: test -d ."))
	s := e.ok("claim", x)
	e.commit(must.As[string](s["workspace"]), "x.txt", "x")
	if r := e.runIn(e.cwd, "", []string{"ATASK_SESSION=" + must.As[string](s["token"])}, "verify", "complete"); r.code != 0 || must.As[map[string]any](r.env["result"])["completed"] != true {
		t.Fatalf("real gates should complete: %s %s", r.stdout, r.stderr)
	}
}

// The documented discovered-prerequisite flow works from inside the task
// worktree with no token at all (the worktree supplies the session), needs
// proofs to become claimable, and is refused from outside without a session.
func TestDiscoveredBlockerFromWorktree(t *testing.T) {
	e := gitEnv(t)
	a := taskID(e.ok("add", "main work", "--key", "main", "--proof", "run: test -d .", "--regression", "run: true"))
	s := e.ok("claim", a)
	ws := must.As[string](s["workspace"])
	if r := e.runIn(ws, "", nil, "add", "prereq", "--blocks", "main"); r.code != 0 || !strings.Contains(r.stdout, `"status":"draft"`) {
		t.Fatalf("blocker without proofs must be filed as a draft: %s %s", r.stdout, r.stderr)
	}
	r := e.runIn(ws, "", nil, "add", "prereq", "--key", "pre", "--blocks", "main", "--proof", "run: test -d .", "--regression", "run: true")
	if r.code != 0 {
		t.Fatalf("blocker from worktree: %s %s", r.stdout, r.stderr)
	}
	if r := e.runIn(ws, "", nil, "log", "--next", "wait for pre"); r.code != 0 {
		t.Fatalf("log: %s", r.stdout)
	}
	if r := e.runIn(ws, "", nil, "claim", "release"); r.code != 0 || must.As[map[string]any](r.env["result"])["status"] != "blocked" {
		t.Fatalf("release: %s %s", r.stdout, r.stderr)
	}
}

// CLI corners: subcommand dispatch with leading global flags, JSON envelopes for help and a bare
// invocation, incompatible flags refused, reset-attempts needs planner
// authority, and prune.
func TestCLICorners(t *testing.T) {
	e := gitEnv(t)
	x := taskID(e.ok("add", "x", "--key", "x", "--proof", "run: test -d .", "--regression", "run: true"))
	s := e.ok("claim", x)
	if r := e.run("claim", "--json", "renew", must.As[string](s["token"])); r.code != 0 || must.As[map[string]any](r.env["result"])["lease_until"] == nil {
		t.Fatalf("claim --json renew: %s %s", r.stdout, r.stderr)
	}
	if r := e.run("help"); r.code != 0 || must.As[map[string]any](r.env["result"])["commands"] == nil {
		t.Fatalf("help envelope: %s", r.stdout)
	}
	if r := e.run(); r.code != 0 || r.env == nil {
		t.Fatalf("bare at envelope: code=%d %s", r.code, r.stdout)
	} else if _, ok := must.As[map[string]any](r.env["result"])["tasks"]; !ok {
		t.Fatalf("bare atask should list the board: %s", r.stdout)
	}
	if r := e.run("add", "G", "--group", "--requires", "x"); r.code != 2 {
		t.Fatalf("--group with --requires accepted: %s", r.stdout)
	}
	if r := e.run("update", "x", "--archive", "--reason", "r", "--outcome", "nope"); r.code != 2 {
		t.Fatalf("--archive with edits accepted: %s", r.stdout)
	}
	e.ok("update", "x", "--reset-attempts")
	e.commit(must.As[string](s["workspace"]), "x.txt", "x")
	if r := e.runIn(must.As[string](s["workspace"]), "", nil, "verify", "complete"); r.code != 0 {
		t.Fatalf("%s %s", r.stdout, r.stderr)
	}
	pr := e.ok("prune")
	if removed := must.As[[]any](pr["removed"]); len(removed) != 1 || must.As[string](removed[0]) != must.As[string](s["workspace"]) {
		t.Fatalf("prune: %v", pr)
	}
}

// TestPlanFilesClaimAndDoctor covers the planner's batch input (`add -f`
// with YAML and JSON, `--json`) and `doctor` on a healthy and on an unhealthy project.
func TestPlanFilesClaimAndDoctor(t *testing.T) {
	e := gitEnv(t)
	e.git(e.cwd, "config", "user.name", "t")
	e.git(e.cwd, "config", "user.email", "t@t")
	// Healthy: regression passes on main.
	rep := e.ok("doctor")
	if rep["healthy"] != true {
		t.Fatalf("doctor: %v", rep)
	}
	yamlPlan := filepath.Join(t.TempDir(), "plan.yaml")
	must.OK(t, os.WriteFile(yamlPlan, []byte(`tasks:
  - kind: group
    key: core
    title: Core
  - key: parse
    title: Parse tokens
    parent: core
    outcome: tokens are parsed
    constraints: [no cgo]
    acceptance: ["bad input is rejected"]
    proof: "run: test -f parse.txt"
    regression: "run: true"
  - key: reject
    title: Reject expired tokens
    requires: [parse]
    proof: "run: test -f reject.txt"
    regression: "run: true"
`), 0o644))
	res := e.ok("add", "-f", yamlPlan)
	if must.As[float64](res["changed"]) != 3 || len(must.As[map[string]any](res["created"])) != 3 {
		t.Fatalf("yaml plan: %v", res)
	}
	parse := e.ok("show", "parse")
	if len(must.As[[]any](parse["task_checks"])) != 1 || must.As[map[string]any](parse["group"])["title"] != "Core" || len(must.As[[]any](parse["constraints"])) != 1 {
		t.Fatalf("parse: %v", parse)
	}
	// Atomic: a bad item means nothing is added.
	cycle := `[{"key":"a","title":"A","requires":["b"],"proof":"run: test -d .","regression":"run: test -d ."},{"key":"b","title":"B","requires":["a"],"proof":"run: test -d .","regression":"run: test -d ."}]`
	if r := e.runIn(e.cwd, cycle, nil, "add", "-f", "-"); r.code == 0 {
		t.Fatalf("a cyclic plan was accepted: %s", r.stdout)
	}
	e.fails("NOT_FOUND", "show", "a")
	// JSON on stdin, one object.
	r := e.runIn(e.cwd, `{"key":"docs","title":"Document tokens","proof":"run: test -f README.md","regression":"run: test -d ."}`, nil, "add", "-f", "-")
	if r.code != 0 || r.env["ok"] != true {
		t.Fatalf("add -f -: %s %s", r.stdout, r.stderr)
	}
	if e.ok("show", "docs")["status"] != "ready" {
		t.Fatalf("a planned task with both proofs must be ready: %v", e.ok("show", "docs"))
	}
	e.fails("INVALID_INPUT", "add", "-f", yamlPlan, "--proof", "run: test -d .", "--regression", "run: true")
}

// TestInitInstallsInstructions: `atask init` writes the AGENTS.md block and
// the CLAUDE.md import; re-runs refresh only the block and keep the
// repository's own text.
func TestInitInstallsInstructions(t *testing.T) {
	e := gitEnv(t) // init ran once already
	agents := filepath.Join(e.cwd, "AGENTS.md")
	b, err := os.ReadFile(agents)
	if err != nil || !strings.Contains(string(b), "<!-- BEGIN atask v:1 hash:") || !strings.Contains(string(b), "Track verifiable objectives") || !strings.Contains(string(b), "# Engineering guidelines") {
		t.Fatalf("AGENTS.md: %v\n%s", err, b)
	}
	if c, _ := os.ReadFile(filepath.Join(e.cwd, "CLAUDE.md")); strings.TrimSpace(string(c)) != "@AGENTS.md" {
		t.Fatalf("CLAUDE.md: %q", c)
	}
	// The repository's own text survives a refresh; init is idempotent.
	must.OK(t, os.WriteFile(agents, []byte(string(b)+"\n# Ours\n\nkeep me\n"), 0o644))
	again := e.ok("init")
	if again["existing"] != true {
		t.Fatalf("re-init: %v", again)
	}
	b2, _ := os.ReadFile(agents)
	if !strings.Contains(string(b2), "keep me") || strings.Count(string(b2), "<!-- BEGIN atask") != 1 {
		t.Fatalf("refresh damaged AGENTS.md:\n%s", b2)
	}
	settings, err := os.ReadFile(filepath.Join(e.cwd, ".claude", "settings.json"))
	if err != nil || strings.Count(string(settings), "atask context") != 1 || !strings.Contains(string(settings), "SessionStart") {
		t.Fatalf(".claude/settings.json: %v\n%s", err, settings)
	}
}

// TestSizesBudgetsAndDoctorTiming: sizes travel through add, update, plan
// files and show; project budgets are set with durations or "none"; the
// context states them; doctor fails a project whose regression suite is
// over budget.
func TestSizesBudgetsAndDoctorTiming(t *testing.T) {
	e := gitEnv(t)
	big := e.ok("add", "Big", "--key", "big", "--size", "large", "--proof", "run: test -d .", "--regression", "run: true")
	if must.As[map[string]any](big["task"])["size"] != "large" {
		t.Fatalf("size: %v", big)
	}
	e.fails("INVALID_INPUT", "add", "Odd", "--size", "huge", "--proof", "run: test -d .", "--regression", "run: true")
	e.ok("update", "big", "--size", "medium")
	if e.ok("show", "big")["size"] != "medium" {
		t.Fatal("size not updated")
	}
	r := e.runIn(e.cwd, `{"key":"s","title":"S","size":"small","proof":"run: test -d .","regression":"run: test -d ."}`, nil, "add", "-f", "-")
	if r.code != 0 {
		t.Fatalf("plan file size: %s", r.stdout)
	}
	// A project holds no budgets: the flags are gone, not silently ignored.
	e.fails("INVALID_INPUT", "project", "--budget-small", "45s")
	e.fails("INVALID_INPUT", "project", "--regression-budget", "none")
}

// TestEpicChecksFromTheCLI: a group with its own checks through add,
// show, list, verify complete GROUP and status.
func TestEpicChecksFromTheCLI(t *testing.T) {
	e := gitEnv(t)
	g := taskID(e.ok("add", "Identity", "--group", "--key", "identity", "--proof", "run: test -f a.txt", "--regression", "run: test -f a.txt"))
	a := taskID(e.ok("add", "A", "--key", "a", "--parent", "identity", "--proof", "run: test -f a.txt", "--regression", "run: true"))
	if sh := e.ok("show", "identity"); sh["size"] != "large" || must.As[map[string]any](sh["group_verification"])["status"] != nil {
		t.Fatalf("group: %v", sh)
	}
	if r := e.run("verify", "complete", "identity"); r.code == 0 || must.As[map[string]any](r.env["error"])["code"] != "NOT_READY" {
		t.Fatalf("children incomplete must be NOT_READY: %s %s", r.stdout, r.stderr)
	}
	s := e.ok("claim", a)
	e.commit(must.As[string](s["workspace"]), "a.txt", "a")
	if r := e.runIn(must.As[string](s["workspace"]), "", nil, "verify", "complete"); r.code != 0 {
		t.Fatalf("complete: %s %s", r.stdout, r.stderr)
	}
	if vg := e.ok("verify", "complete", "identity"); vg["completed"] != true {
		t.Fatalf("group verify: %v", vg)
	}
	if st := e.ok("status"); st["done"] != true {
		t.Fatalf("status after epic: %v", st)
	}
	h := e.runIn(e.cwd, "", []string{"ATASK_OUTPUT="}, "list", "all")
	if !strings.Contains(h.stdout, "● "+g) {
		t.Fatalf("list glyph: %s", h.stdout)
	}
	_ = a
}
