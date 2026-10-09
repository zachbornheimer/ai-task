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
	if !strings.HasSuffix(sess["branch"].(string), "/"+a) || !strings.HasPrefix(sess["branch"].(string), "at/") {
		t.Fatalf("branch %v", sess["branch"])
	}
	// A commit in the worktree renews the lease through the post-commit hook.
	before := e.ok("show", a)["attempt"].(map[string]any)["lease_expires_at"]
	e.commit(ws, "hook.txt", "h")
	if after := e.ok("show", a)["attempt"].(map[string]any)["lease_expires_at"]; after == before {
		t.Fatalf("post-commit hook did not renew the lease: %v == %v", before, after)
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
	// Human views carry the glyph convention.
	h := e.runIn(e.cwd, "", []string{"AT_OUTPUT="}, "list", "all")
	if !strings.Contains(h.stdout, "● "+a) || !strings.Contains(h.stdout, "○ "+b) {
		t.Fatalf("list glyphs:\n%s", h.stdout)
	}
	hs := e.runIn(e.cwd, "", []string{"AT_OUTPUT="}, "show", "reject")
	if !strings.Contains(hs.stdout, "○ "+b+" · Reject expired access tokens") || !strings.Contains(hs.stdout, "REQUIRES\n● "+a) {
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
