// Package e2e runs the built `at` binary as real subprocesses: process
// boundaries, environment defaults, exit codes, stdout/stderr separation,
// token transports, and multi-process claims on one database.
package e2e

import (
	"bytes"
	"encoding/json"
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
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "at-e2e-")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "at")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/at")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("build: " + err.Error())
	}
	code := m.Run()
	os.RemoveAll(dir)
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
	os.MkdirAll(cwd, 0o755)
	e := &env{t: t, db: filepath.Join(base, "state", "at.db"), cwd: cwd}
	e.env = append(os.Environ(), "AT_DB="+e.db, "AT_OUTPUT=json", "AT_PROJECT=", "AT_SESSION=")
	return e
}

// gitEnv makes the working directory a Git repository with one commit and
// registers it with a passing regression check.
func gitEnv(t *testing.T) *env {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	e := newEnv(t)
	e.git(e.cwd, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(e.cwd, "f.txt"), []byte("one"), 0o644)
	e.git(e.cwd, "add", "f.txt")
	e.git(e.cwd, "commit", "-q", "-m", "one")
	e.ok("init", "--name", "g")
	e.ok("project", "--regression-check", "regress: true")
	return e
}

func (e *env) git(dir string, args ...string) {
	e.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	// Hooks see the test environment and find the built `at` on PATH.
	cmd.Env = append(append([]string{}, e.env...), "PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		e.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (e *env) commit(dir, file, content string) {
	e.t.Helper()
	os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644)
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
	if ee, ok := err.(*exec.ExitError); ok {
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
		return t["id"].(string)
	}
	return m["id"].(string)
}

func TestAgentLoopEndToEnd(t *testing.T) {
	e := gitEnv(t)
	a := taskID(e.ok("add", "Parse tokens", "--key", "parse", "--check", "unit: test -f parse.txt"))
	b := taskID(e.ok("add", "Reject expired access tokens", "--key", "reject", "--requires", "parse", "--accept", "expired -> 401", "--check", "unit: test -f reject.txt"))
	e.fails("TASK_BLOCKED", "claim", b)
	e.fails("DEPENDENCY_CYCLE", "update", "parse", "--requires", "reject")
	e.fails("INVALID_INPUT", "verify")
	e.fails("INVALID_INPUT", "verify", "complete") // no session anywhere -> usage
	sess := e.ok("claim")
	tok := sess["token"].(string)
	ws := sess["workspace"].(string)
	if taskID(sess) != a || !strings.HasPrefix(tok, "sess-") || ws == "" {
		t.Fatalf("claim: %v", sess)
	}
	// The claim says where work lands and which regression checks will run.
	if sess["target_branch"] != "main" || sess["target_revision"] == "" || len(sess["regression_checks"].([]any)) == 0 {
		t.Fatalf("claim lacks target or regression checks: %v", sess)
	}
	with := []string{"AT_SESSION=" + tok}
	// log via AT_SESSION, positional, --session, and stdin.
	if r := e.runIn(e.cwd, "", with, "log", "--done", "started", "--next", "write parse.txt"); r.code != 0 {
		t.Fatalf("env token: %s %s", r.stdout, r.stderr)
	}
	e.ok("log", tok, "--learned", "positional token works")
	e.ok("log", "--session", tok, "--note", "flag token works")
	if r := e.runIn(e.cwd, tok+"\n", nil, "log", "-", "--note", "stdin works"); r.code != 0 {
		t.Fatalf("stdin token: %s %s", r.stdout, r.stderr)
	}
	// Inside the worktree no token is needed: the claim stored it there.
	if r := e.runIn(ws, "", nil, "log", "--note", "worktree token works"); r.code != 0 {
		t.Fatalf("worktree token: %s %s", r.stdout, r.stderr)
	}
	if r := e.runIn(ws, "", nil, "whoami"); r.code != 0 || taskID(r.env["result"].(map[string]any)) != a {
		t.Fatalf("whoami in worktree: %s %s", r.stdout, r.stderr)
	}
	// The working context: resolved from the worktree, or by reference
	// from anywhere; the human form is the prompt itself.
	if r := e.runIn(ws, "", nil, "context"); r.code != 0 || !strings.Contains(r.env["result"].(map[string]any)["prompt"].(string), "# Task "+a) {
		t.Fatalf("context in worktree: %s %s", r.stdout, r.stderr)
	}
	if cx := e.ok("context", b, "--with-rules"); !strings.Contains(cx["prompt"].(string), "Agent execution contract") || cx["workspace"] != nil {
		t.Fatalf("context by id: %v", cx)
	}
	if cx := e.ok("context"); cx["task"] != nil || cx["summary"] == nil || !strings.Contains(cx["prompt"].(string), "no task in hand") {
		t.Fatalf("project context: %v", cx)
	}
	if r := e.runIn(ws, "", []string{"AT_OUTPUT="}, "context"); r.code != 0 || !strings.HasPrefix(r.stdout, "# Task "+a) {
		t.Fatalf("human context: %s %s", r.stdout, r.stderr)
	}
	if !strings.HasSuffix(sess["branch"].(string), "/"+a) || !strings.HasPrefix(sess["branch"].(string), "at/") {
		t.Fatalf("branch %v", sess["branch"])
	}
	if _, err := os.Stat(filepath.Join(e.cwd, ".git", "hooks", "post-commit")); err == nil {
		t.Fatal("init must not write hooks into the repository")
	}
	// Provisional checks run and never complete.
	r := e.runIn(e.cwd, "", with, "verify", "task")
	if r.env["ok"] != false || r.env["error"].(map[string]any)["code"] != "VERIFICATION_FAILED" {
		t.Fatalf("verify task before work: %s", r.stdout)
	}
	e.commit(ws, "parse.txt", "p")
	if r := e.runIn(e.cwd, "", with, "verify", "task"); r.code != 0 || r.env["result"].(map[string]any)["completed"] != false {
		t.Fatalf("verify task: %s", r.stdout)
	}
	if r := e.runIn(e.cwd, "", with, "verify", "regression"); r.code != 0 {
		t.Fatalf("verify regression: %s %s", r.stdout, r.stderr)
	}
	if e.ok("show", a)["status"] != "claimed" {
		t.Fatal("diagnostics must not change status")
	}
	// verify complete: completes, ends the claim, promotes to main.
	r = e.runIn(e.cwd, "", with, "verify", "complete")
	if r.code != 0 || r.env["result"].(map[string]any)["completed"] != true {
		t.Fatalf("verify complete: %s %s", r.stdout, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(e.cwd, "parse.txt")); err != nil {
		t.Fatal("not promoted to main")
	}
	if r := e.runIn(e.cwd, "", with, "log", "--done", "late"); r.env["error"].(map[string]any)["code"] != "SESSION_FINISHED" {
		t.Fatalf("%s", r.stdout)
	}
	// Dependent is ready; outside a worktree, renew/release need a token.
	e.fails("INVALID_INPUT", "claim", "renew")
	sb := e.ok("claim", "reject")
	e.ok("claim", "renew", sb["token"].(string))
	if r := e.runIn(sb["workspace"].(string), "", nil, "claim", "renew"); r.code != 0 {
		t.Fatalf("renew inside worktree: %s %s", r.stdout, r.stderr)
	}
	// A token used inside another task's worktree is refused before any
	// write: the confused-deputy case of a mis-exported AT_SESSION.
	if r := e.runIn(sb["workspace"].(string), "", with, "log", "--note", "wrong worktree"); r.env["error"] == nil || r.env["error"].(map[string]any)["code"] != "INVALID_SESSION" {
		t.Fatalf("token for %s accepted in %s's worktree: %s", a, b, r.stdout)
	}
	e.fails("INVALID_SESSION", "claim", "renew", "sess-0000000000000000000000000000000a")
	e.fails("SESSION_FINISHED", "claim", "release", tok)
	rel := e.ok("claim", "release", sb["token"].(string), "--note", "later")
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
	h := e.runIn(e.cwd, "", []string{"AT_OUTPUT="}, "list", "all")
	if !strings.Contains(h.stdout, "● "+a) || !strings.Contains(h.stdout, "◐ "+b) {
		t.Fatalf("list glyphs:\n%s", h.stdout)
	}
	hs := e.runIn(e.cwd, "", []string{"AT_OUTPUT="}, "show", "reject")
	if !strings.Contains(hs.stdout, "◐ "+b+" · Reject expired access tokens") || !strings.Contains(hs.stdout, "REQUIRES\n● "+a) {
		t.Fatalf("show:\n%s", hs.stdout)
	}
	st := e.ok("status")
	if st["done"] != false || st["claimable"].(float64) != 1 {
		t.Fatalf("status: %v", st)
	}
}

func TestPlanningCommands(t *testing.T) {
	e := newEnv(t)
	e.ok("init", "--name", "p") // not a repository yet: init creates one
	g := taskID(e.ok("--project", "p", "add", "Identity Core", "--group", "--key", "identity"))
	a := taskID(e.ok("--project", "p", "add", "Rotate creds", "--key", "rotate", "--check", "u: true"))
	b := taskID(e.ok("--project", "p", "add", "Token store", "--key", "store", "--parent", "identity", "--requires", "rotate", "--check", "u: true"))
	e.fails("DUPLICATE_KEY", "--project", "p", "add", "Token store v2", "--key", "store", "--check", "u: true")
	e.fails("MISSING_VERIFICATION", "--project", "p", "add", "No checks", "--key", "nochecks")
	rep := e.ok("--project", "p", "add", "Token store", "--key", "store", "--parent", "identity", "--requires", "rotate", "--check", "u: true")
	if rep["changed"].(float64) != 0 {
		t.Fatalf("identical replay changed the plan: %v", rep)
	}
	e.fails("PLAN_CONFLICT", "--project", "p", "update", "store", "--title", "x", "--expect-rev", "1")
	e.fails("INVALID_INPUT", "--project", "p", "update", "rotate", "--archive")                               // reason required
	e.fails("PLAN_CONFLICT", "--project", "p", "update", "rotate", "--archive", "--reason", "done elsewhere") // store requires it
	up := e.ok("--project", "p", "update", "store", "--remove-requires", "rotate", "--accept", "works")
	if up["changed"].(float64) != 1 {
		t.Fatalf("%v", up)
	}
	e.ok("--project", "p", "update", "rotate", "--archive", "--reason", "done elsewhere")
	if sh := e.ok("--project", "p", "show", a); sh["status"] != "archived" || sh["archive_reason"] != "done elsewhere" {
		t.Fatalf("not archived with reason: %v", sh)
	}
	snap := e.ok("--project", "p", "list", "all")
	if len(snap["groups"].([]any)) != 1 || len(snap["tasks"].([]any)) != 1 {
		t.Fatalf("%v", snap)
	}
	h := e.runIn(e.cwd, "", []string{"AT_OUTPUT="}, "--project", "p", "list")
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
	if r := e.run("--project", "p", "show", "at-000000"); r.code != 1 || r.env["error"].(map[string]any)["code"] != "NOT_FOUND" {
		t.Fatalf("%+v", r)
	}
}

func TestConcurrentProcessesClaimDistinctTasks(t *testing.T) {
	e := newEnv(t)
	e.ok("init", "--name", "c")
	e.fails("MISSING_VERIFICATION", "claim") // no regression checks yet
	e.ok("project", "--regression-check", "regress: true")
	const n = 6
	ids := map[string]bool{}
	for i := 0; i < n; i++ {
		ids[taskID(e.ok("add", "work", "--check", "u: true"))] = true
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
			seen[taskID(r.env["result"].(map[string]any))]++
		} else if r.env["error"].(map[string]any)["code"] == "NO_ELIGIBLE_WORK" {
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
	script := bin + " --db " + e.db + " --project g add from-inside-check --check 'u: true'"
	a := taskID(e.ok("add", "reentrant", "--check", "nested: sh -c "+shellQuote(script)))
	s := e.ok("claim", a)
	e.commit(s["workspace"].(string), "r.txt", "r")
	r := e.runIn(e.cwd, "", []string{"AT_SESSION=" + s["token"].(string)}, "verify", "complete")
	if r.code != 0 {
		t.Fatalf("%s %s", r.stdout, r.stderr)
	}
	if !strings.Contains(e.run("list", "all").stdout, "from-inside-check") {
		t.Fatal("nested write did not land")
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Every CLI route that defines checks enforces a required check: optional
// checks are informational and JSON without "required" means required.
func TestVacuousChecksRejectedByCLI(t *testing.T) {
	e := gitEnv(t)
	e.fails("MISSING_VERIFICATION", "add", "opt only", "--optional-check", "o: false")
	e.fails("MISSING_VERIFICATION", "add", "json false", "--policy-json", `{"task_checks":[{"id":"j","command":["true"],"required":false}]}`)
	x := taskID(e.ok("add", "json absent", "--policy-json", `{"task_checks":[{"id":"j","command":["true"]}]}`))
	checks := e.ok("show", x)["task_checks"].([]any)
	if len(checks) != 1 || checks[0].(map[string]any)["required"] != true {
		t.Fatalf("absent required must mean required: %v", checks)
	}
	e.fails("MISSING_VERIFICATION", "update", x, "--optional-check", "o: true")
	e.fails("MISSING_VERIFICATION", "project", "--regression-json", `[{"id":"r2","command":["true"],"required":false}]`)
	e.ok("project", "--regression-json", `[{"id":"r2","command":["true"]}]`)
	s := e.ok("claim", x)
	e.commit(s["workspace"].(string), "x.txt", "x")
	if r := e.runIn(e.cwd, "", []string{"AT_SESSION=" + s["token"].(string)}, "verify", "complete"); r.code != 0 || r.env["result"].(map[string]any)["completed"] != true {
		t.Fatalf("real gates should complete: %s %s", r.stdout, r.stderr)
	}
}

// The documented discovered-prerequisite flow works from inside the task
// worktree with no token at all (the worktree supplies the session), needs
// a check like any task, and is refused from outside without a session.
func TestDiscoveredBlockerFromWorktree(t *testing.T) {
	e := gitEnv(t)
	a := taskID(e.ok("add", "main work", "--key", "main", "--check", "u: true"))
	s := e.ok("claim", a)
	ws := s["workspace"].(string)
	e.fails("PLAN_CONFLICT", "add", "prereq", "--blocks", "main", "--check", "u: true") // from the main checkout, no session
	if r := e.runIn(ws, "", nil, "add", "prereq", "--blocks", "main"); r.env["error"] == nil || r.env["error"].(map[string]any)["code"] != "MISSING_VERIFICATION" {
		t.Fatalf("blocker without a check accepted: %s", r.stdout)
	}
	r := e.runIn(ws, "", nil, "add", "prereq", "--key", "pre", "--blocks", "main", "--check", "u: true")
	if r.code != 0 {
		t.Fatalf("blocker from worktree: %s %s", r.stdout, r.stderr)
	}
	if r := e.runIn(ws, "", nil, "log", "--next", "wait for pre"); r.code != 0 {
		t.Fatalf("log: %s", r.stdout)
	}
	if r := e.runIn(ws, "", nil, "claim", "release"); r.code != 0 || r.env["result"].(map[string]any)["status"] != "blocked" {
		t.Fatalf("release: %s %s", r.stdout, r.stderr)
	}
}

// CLI corners: subcommand dispatch with leading global flags, check
// labels versus colons in commands, JSON envelopes for help and a bare
// invocation, incompatible flags refused, reset-attempts needs planner
// authority, and prune.
func TestCLICorners(t *testing.T) {
	e := gitEnv(t)
	// Check labels: "<id>: " is a label; a colon inside a command (a URL) is
	// not; surrounding whitespace is ignored.
	y := taskID(e.ok("add", "y", "--key", "y", "--check", "u: true", "--check", "https://example.com/health", "--check", "  spaced: true"))
	checks := e.ok("show", y)["task_checks"].([]any)
	ids := []string{}
	for _, c := range checks {
		ids = append(ids, c.(map[string]any)["id"].(string))
	}
	if strings.Join(ids, ",") != "u,check2,spaced" {
		t.Fatalf("check ids: %v", ids)
	}
	if cmd := checks[1].(map[string]any)["command"].([]any); len(cmd) != 1 || cmd[0] != "https://example.com/health" {
		t.Fatalf("url command split: %v", cmd)
	}
	x := taskID(e.ok("add", "x", "--key", "x", "--check", "u: true"))
	s := e.ok("claim", x)
	if r := e.run("claim", "--json", "renew", s["token"].(string)); r.code != 0 || r.env["result"].(map[string]any)["lease_until"] == nil {
		t.Fatalf("claim --json renew: %s %s", r.stdout, r.stderr)
	}
	if r := e.run("help"); r.code != 0 || r.env["result"].(map[string]any)["commands"] == nil {
		t.Fatalf("help envelope: %s", r.stdout)
	}
	if r := e.run(); r.code != 2 || r.env == nil || r.env["error"].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("bare at envelope: code=%d %s", r.code, r.stdout)
	}
	if r := e.run("add", "G", "--group", "--check", "u: true"); r.code != 2 {
		t.Fatalf("--group with --check accepted: %s", r.stdout)
	}
	if r := e.run("update", "x", "--archive", "--reason", "r", "--title", "nope"); r.code != 2 {
		t.Fatalf("--archive with edits accepted: %s", r.stdout)
	}
	e.fails("PLAN_CONFLICT", "update", "x", "--reset-attempts")
	e.ok("update", "x", "--reset-attempts", "--planner")
	e.commit(s["workspace"].(string), "x.txt", "x")
	if r := e.runIn(s["workspace"].(string), "", nil, "verify", "complete"); r.code != 0 {
		t.Fatalf("%s %s", r.stdout, r.stderr)
	}
	pr := e.ok("prune")
	if removed := pr["removed"].([]any); len(removed) != 1 || removed[0].(string) != s["workspace"].(string) {
		t.Fatalf("prune: %v", pr)
	}
}

// TestPlanFilesClaimAndDoctor covers the planner's batch input (`add -f`
// with YAML and JSON, `--json`), the `add --claim` fast path, and
// `doctor` on a healthy and on an unhealthy project.
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
	os.WriteFile(yamlPlan, []byte(`tasks:
  - kind: group
    key: core
    title: Core
  - key: parse
    title: Parse tokens
    parent: core
    outcome: tokens are parsed
    constraints: [no cgo]
    acceptance: ["bad input is rejected"]
    checks:
      - "unit: test -f parse.txt"
      - id: shape
        command: [sh, -c, "test -f parse.txt"]
        required: false
  - key: reject
    title: Reject expired tokens
    requires: [parse]
    checks: ["unit: test -f reject.txt"]
`), 0o644)
	res := e.ok("add", "-f", yamlPlan)
	if res["changed"].(float64) != 3 || len(res["created"].(map[string]any)) != 3 {
		t.Fatalf("yaml plan: %v", res)
	}
	parse := e.ok("show", "parse")
	if len(parse["task_checks"].([]any)) != 2 || parse["group"].(map[string]any)["title"] != "Core" || len(parse["constraints"].([]any)) != 1 {
		t.Fatalf("parse: %v", parse)
	}
	// Atomic: a bad item means nothing is added.
	e.fails("DEPENDENCY_CYCLE", "add", "--plan", `[{"key":"a","title":"A","requires":["b"],"checks":["u: true"]},{"key":"b","title":"B","requires":["a"],"checks":["u: true"]}]`)
	e.fails("NOT_FOUND", "show", "a")
	// JSON on stdin, one object, and --claim hands back the session.
	r := e.runIn(e.cwd, `{"key":"docs","title":"Document tokens","checks":["readme: test -f README.md"]}`, nil, "add", "-f", "-", "--claim")
	if r.code != 0 || r.env["ok"] != true {
		t.Fatalf("add -f - --claim: %s %s", r.stdout, r.stderr)
	}
	out := r.env["result"].(map[string]any)
	sess, _ := out["session"].(map[string]any)
	if sess == nil || sess["workspace"] == "" || taskID(sess) != taskID(out) {
		t.Fatalf("claim in add: %v", out)
	}
	if e.ok("show", "docs")["status"] != "claimed" {
		t.Fatal("not claimed")
	}
	// Pins are part of the contract and reach the agent's context.
	pinned := e.ok("add", "Pinned", "--key", "pinned", "--check", "u: true", "--pin", "checks/", "--pin", "go.mod")
	if pins := pinned["task"].(map[string]any)["pins"].([]any); len(pins) != 2 || pins[0] != "checks" {
		t.Fatalf("pins: %v", pinned)
	}
	if !strings.Contains(e.ok("context", "pinned")["prompt"].(string), "Pinned (this task may not change them") {
		t.Fatal("context lacks pins")
	}
	e.ok("update", "pinned", "--pin", "", "--planner")
	if e.ok("show", "pinned")["pins"] != nil {
		t.Fatal("pins not cleared")
	}
	// Flags with --claim too.
	fast := e.ok("add", "Fast path", "--check", "u: true", "--claim")
	if fast["session"] == nil {
		t.Fatalf("flags --claim: %v", fast)
	}
	e.fails("INVALID_INPUT", "add", "-f", yamlPlan, "--check", "u: true")
	// Unhealthy: the regression suite fails on main; doctor says so and
	// names the check, before any task is charged for it.
	e.ok("project", "--regression-check", "regress: false")
	rr := e.fails("UNHEALTHY", "doctor")
	det := rr.env["error"].(map[string]any)["details"].(map[string]any)
	if det["healthy"] != false {
		t.Fatalf("doctor details: %v", det)
	}
	found := false
	for _, ch := range det["checks"].([]any) {
		m := ch.(map[string]any)
		if m["id"] == "baseline" && m["ok"] == false && strings.Contains(m["message"].(string), "regress") {
			found = true
		}
	}
	if !found {
		t.Fatalf("baseline not reported: %v", det["checks"])
	}
	if e.ok("doctor", "--skip-baseline")["healthy"] != true {
		t.Fatal("--skip-baseline should not run the suite")
	}
}

// TestInitInstallsInstructionsAndHooks: `at init` writes the AGENTS.md
// block and the CLAUDE.md import, re-runs refresh only the block and keep
// the repository's own text, `--claude` merges the hooks into
// .claude/settings.json beside existing settings, and `at hook` answers
// Claude Code's events: context at session start, edits denied outside
// a task worktree and allowed inside one, and a stop refused while the
// worktree holds an unfinished claim.
func TestInitInstallsInstructionsAndHooks(t *testing.T) {
	e := gitEnv(t) // init ran once already
	agents := filepath.Join(e.cwd, "AGENTS.md")
	b, err := os.ReadFile(agents)
	if err != nil || !strings.Contains(string(b), "<!-- at:begin -->") || !strings.Contains(string(b), "## Agent execution contract") || !strings.Contains(string(b), "# Engineering guidelines") {
		t.Fatalf("AGENTS.md: %v\n%s", err, b)
	}
	if c, _ := os.ReadFile(filepath.Join(e.cwd, "CLAUDE.md")); strings.TrimSpace(string(c)) != "@AGENTS.md" {
		t.Fatalf("CLAUDE.md: %q", c)
	}
	// The repository's own text survives a refresh; init is idempotent.
	os.WriteFile(agents, []byte(string(b)+"\n# Ours\n\nkeep me\n"), 0o644)
	os.WriteFile(filepath.Join(e.cwd, ".claude", "settings.json"), nil, 0o644) // will be replaced below
	os.RemoveAll(filepath.Join(e.cwd, ".claude"))
	os.MkdirAll(filepath.Join(e.cwd, ".claude"), 0o755)
	os.WriteFile(filepath.Join(e.cwd, ".claude", "settings.json"), []byte(`{"permissions":{"allow":["Bash(go test *)"]}}`), 0o644)
	again := e.ok("init", "--claude")
	if again["existing"] != true || again["claude_hooks"] == nil {
		t.Fatalf("re-init: %v", again)
	}
	b2, _ := os.ReadFile(agents)
	if !strings.Contains(string(b2), "keep me") || strings.Count(string(b2), "<!-- at:begin -->") != 1 {
		t.Fatalf("refresh damaged AGENTS.md:\n%s", b2)
	}
	var settings map[string]any
	sb, _ := os.ReadFile(filepath.Join(e.cwd, ".claude", "settings.json"))
	if err := json.Unmarshal(sb, &settings); err != nil || settings["permissions"] == nil {
		t.Fatalf("settings merge: %v %s", err, sb)
	}
	hooks := settings["hooks"].(map[string]any)
	for _, ev := range []string{"SessionStart", "PreToolUse", "Stop"} {
		if hooks[ev] == nil {
			t.Fatalf("hook %s missing: %s", ev, sb)
		}
	}
	e.ok("init", "--claude") // idempotent: no duplicate entries
	sb2, _ := os.ReadFile(filepath.Join(e.cwd, ".claude", "settings.json"))
	if strings.Count(string(sb2), "at hook guard-edit") != 1 {
		t.Fatalf("hooks duplicated: %s", sb2)
	}
	// Hooks. Session start with no task: the plan.
	a := taskID(e.ok("add", "Guarded", "--check", "u: test -f g.txt"))
	if r := e.runIn(e.cwd, `{"hook_event_name":"SessionStart","cwd":"`+e.cwd+`"}`, nil, "hook", "session-start"); r.code != 0 || !strings.Contains(r.stdout, "Claimable now") {
		t.Fatalf("session-start: %d %s %s", r.code, r.stdout, r.stderr)
	}
	// Edit in the project root: denied; outside any project: allowed.
	deny := e.runIn(e.cwd, `{"tool_name":"Write","tool_input":{"file_path":"`+filepath.Join(e.cwd, "f.txt")+`"},"cwd":"`+e.cwd+`"}`, nil, "hook", "guard-edit")
	if deny.code != 0 || !strings.Contains(deny.stdout, `"permissionDecision":"deny"`) {
		t.Fatalf("guard-edit in root: %d %s %s", deny.code, deny.stdout, deny.stderr)
	}
	if r := e.runIn(e.cwd, `{"tool_name":"Write","tool_input":{"file_path":"`+filepath.Join(t.TempDir(), "x.txt")+`"},"cwd":"`+e.cwd+`"}`, nil, "hook", "guard-edit"); r.code != 0 || strings.TrimSpace(r.stdout) != "" {
		t.Fatalf("guard-edit outside: %d %s", r.code, r.stdout)
	}
	if r := e.runIn(e.cwd, `{"tool_name":"Write","tool_input":{"file_path":"`+filepath.Join(e.cwd, "f.txt")+`"},"cwd":"`+e.cwd+`"}`, []string{"AT_ALLOW_DIRECT=1"}, "hook", "guard-edit"); strings.TrimSpace(r.stdout) != "" {
		t.Fatalf("AT_ALLOW_DIRECT ignored: %s", r.stdout)
	}
	// Inside the task worktree: allowed; stop refused while the claim is live.
	sess := e.ok("claim", a)
	ws := sess["workspace"].(string)
	if r := e.runIn(ws, `{"tool_name":"Edit","tool_input":{"file_path":"`+filepath.Join(ws, "g.txt")+`"},"cwd":"`+ws+`"}`, nil, "hook", "guard-edit"); r.code != 0 || strings.TrimSpace(r.stdout) != "" {
		t.Fatalf("guard-edit in worktree: %d %s", r.code, r.stdout)
	}
	if r := e.runIn(ws, `{"hook_event_name":"SessionStart","cwd":"`+ws+`"}`, nil, "hook", "session-start"); !strings.Contains(r.stdout, "# Task "+a) {
		t.Fatalf("session-start in worktree: %s", r.stdout)
	}
	stop := e.runIn(ws, `{"hook_event_name":"Stop","stop_hook_active":false,"cwd":"`+ws+`"}`, nil, "hook", "stop")
	if !strings.Contains(stop.stdout, `"decision":"block"`) || !strings.Contains(stop.stdout, a) {
		t.Fatalf("stop with live claim: %s", stop.stdout)
	}
	if r := e.runIn(ws, `{"hook_event_name":"Stop","stop_hook_active":true,"cwd":"`+ws+`"}`, nil, "hook", "stop"); strings.TrimSpace(r.stdout) != "" {
		t.Fatalf("stop_hook_active must end the loop: %s", r.stdout)
	}
	e.commit(ws, "g.txt", "g")
	if r := e.runIn(ws, "", nil, "verify", "complete"); r.code != 0 {
		t.Fatalf("verify complete: %s %s", r.stdout, r.stderr)
	}
	if r := e.runIn(ws, `{"hook_event_name":"Stop","stop_hook_active":false,"cwd":"`+ws+`"}`, nil, "hook", "stop"); strings.TrimSpace(r.stdout) != "" {
		t.Fatalf("stop after completion must allow: %s", r.stdout)
	}
	// --no-instructions writes nothing.
	bare := newEnv(t)
	bare.ok("init", "--name", "bare", "--no-instructions")
	if _, err := os.Stat(filepath.Join(bare.cwd, "AGENTS.md")); err == nil {
		t.Fatal("--no-instructions wrote AGENTS.md")
	}
}

// TestSizesBudgetsAndDoctorTiming: sizes travel through add, update, plan
// files and show; project budgets are set with durations or "none"; the
// context states them; doctor fails a project whose regression suite is
// over budget.
func TestSizesBudgetsAndDoctorTiming(t *testing.T) {
	e := gitEnv(t)
	big := e.ok("add", "Big", "--key", "big", "--size", "large", "--check", "u: true")
	if big["task"].(map[string]any)["size"] != "large" {
		t.Fatalf("size: %v", big)
	}
	e.fails("INVALID_INPUT", "add", "Odd", "--size", "huge", "--check", "u: true")
	e.ok("update", "big", "--size", "medium", "--planner")
	if e.ok("show", "big")["size"] != "medium" {
		t.Fatal("size not updated")
	}
	r := e.runIn(e.cwd, `{"key":"s","title":"S","size":"small","checks":["u: true"]}`, nil, "add", "-f", "-")
	if r.code != 0 {
		t.Fatalf("plan file size: %s", r.stdout)
	}
	cx := e.ok("context", "big")["prompt"].(string)
	if !strings.Contains(cx, "task checks 5m0s (size medium), regression 2m0s") {
		t.Fatalf("context budgets: %s", cx)
	}
	proj := e.ok("project", "--budget-small", "45s", "--regression-budget", "none")
	b := proj["budgets"].(map[string]any)
	if b["small_ms"].(float64) != 45000 || b["regression_ms"].(float64) != -1 {
		t.Fatalf("budgets: %v", b)
	}
	e.fails("INVALID_INPUT", "project", "--budget-large", "soon")
	// A regression gate over its budget is a doctor error, before any task pays for it.
	e.ok("project", "--regression-budget", "300ms", "--regression-check", "slow: sleep 1")
	rr := e.fails("UNHEALTHY", "doctor")
	found := false
	for _, ch := range rr.env["error"].(map[string]any)["details"].(map[string]any)["checks"].([]any) {
		m := ch.(map[string]any)
		if m["id"] == "regression_budget" && m["ok"] == false && m["severity"] == "error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("doctor did not flag the slow gate: %s", rr.stdout)
	}
}
