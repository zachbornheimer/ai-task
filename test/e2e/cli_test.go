// Package e2e runs the built `tasks` binary as real subprocesses. These tests
// cover what in-process tests cannot: process boundaries, environment
// defaults, exit codes, stdout/stderr separation, and multi-process
// contention on one database file.
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
	"time"

	// The binary is built from source in TestMain; importing the CLI package
	// makes Go's test cache key include every source file the binary
	// depends on, so a change anywhere re-runs these tests.
	_ "github.com/zachbornheimer/ai-task/internal/cli"
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "tasks-e2e-")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "tasks")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/tasks")
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
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, db: filepath.Join(base, "state", "tasks.db"), cwd: cwd}
	e.env = append(os.Environ(), "TASKS_DB="+e.db, "TASKS_OUTPUT=json")
	// Drop any inherited selection so tests are hermetic.
	e.env = append(e.env, "TASKS_PROJECT=", "TASKS_SESSION=")
	return e
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
	return r.env["result"].(map[string]any)
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

func TestWorkflowAcrossProcesses(t *testing.T) {
	e := newEnv(t)
	proj := e.ok("init", "--name", "demo")
	if !strings.HasPrefix(proj["id"].(string), "proj-") {
		t.Fatalf("init: %v", proj)
	}
	a := e.ok("add", "Parse tokens")["id"].(string)
	b := e.ok("add", "Reject expired access tokens", "--accept", "expired -> 401", "--check", "unit: go test ./auth/...")["id"].(string)
	e.ok("deps", "add", b, "--requires", a)
	e.fails("DEPENDENCY_CYCLE", "deps", "add", a, "--requires", b)
	e.fails("DUPLICATE_DEPENDENCY", "deps", "--requires", a, "add", b) // flags may precede positionals
	e.fails("SELF_DEPENDENCY", "deps", "add", a, "--requires", a)

	r := e.run("list", "--available")
	var list []map[string]any
	json.Unmarshal(mustJSON(t, r.env["result"]), &list)
	if len(list) != 1 || list[0]["id"] != a || list[0]["status"] != "available" {
		t.Fatalf("available: %v", list)
	}
	e.fails("TASK_BLOCKED", "take", b)

	sess := e.ok("take")
	token := sess["token"].(string)
	if sess["task_id"] != a || !strings.HasPrefix(token, "sess-") || sess["attempt"].(float64) != 1 {
		t.Fatalf("take: %v", sess)
	}
	e.fails("TASK_ALREADY_TAKEN", "take", a)
	e.fails("NO_AVAILABLE_TASK", "take")

	// Token transports: positional, environment, stdin.
	e.ok("log", token, "--done", "Updated token validation", "--next", "Test malformed expiry values")
	if r := e.runIn(e.cwd, "", []string{"TASKS_SESSION=" + token}, "log", "--learned", "Expiry validation is shared by two middleware paths"); r.code != 0 {
		t.Fatalf("env token: %s %s", r.stdout, r.stderr)
	}
	if r := e.runIn(e.cwd, token+"\n", nil, "log", "-", "--note", "Watch for backward compatibility"); r.code != 0 {
		t.Fatalf("stdin token: %s %s", r.stdout, r.stderr)
	}
	e.fails("INVALID_INPUT", "log", token)
	e.fails("INVALID_SESSION", "log", "sess-0000000000000000000000000000000a", "--done", "x")
	e.ok("renew-task-lease", token, "--lease", "2h")

	// Secrets never leak through read commands.
	for _, args := range [][]string{{"show", a}, {"history", a}, {"list", "--all"}, {"graph", "--format", "json"}} {
		if out := e.run(args...).stdout; strings.Contains(out, "sess-") {
			t.Fatalf("%v leaked a session token: %s", args, out)
		}
	}
	show := e.ok("show", a)
	handoff := show["handoff"].(map[string]any)
	if handoff["latest_next"] != "Test malformed expiry values" || handoff["total_logs"].(float64) != 3 {
		t.Fatalf("handoff: %v", handoff)
	}

	fin := e.ok("finish", token)
	if fin["status"] != "complete" || fin["verification"] != "passed" {
		t.Fatalf("finish: %v", fin)
	}
	again := e.ok("finish", token)
	if again["already_submitted"] != true {
		t.Fatalf("finish must be idempotent: %v", again)
	}
	e.fails("SESSION_FINISHED", "log", token, "--done", "late")
	e.fails("TASK_COMPLETE", "take", a)

	// Completion releases the dependent; finishing with checks awaits verification.
	sb := e.ok("take", b)
	if sb["task_id"] != b {
		t.Fatalf("take b: %v", sb)
	}
	fb := e.ok("finish", sb["token"].(string), "--no-verify")
	if fb["status"] != "awaiting_verification" || fb["verification"] != "pending" {
		t.Fatalf("finish b: %v", fb)
	}
	e.fails("TASK_AWAITING_VERIFICATION", "take", b)

	hist := e.ok("history", a)
	if hist["total"].(float64) != 3 {
		t.Fatalf("history: %v", hist)
	}
	e.ok("graph")
	dot := e.runIn(e.cwd, "", []string{"TASKS_OUTPUT="}, "graph", "--format", "dot")
	if !strings.Contains(dot.stdout, "digraph tasks") || !strings.Contains(dot.stdout, `"`+b+`" -> "`+a+`"`) {
		t.Fatalf("dot: %s", dot.stdout)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOutputModesAndExitCodes(t *testing.T) {
	e := newEnv(t)
	// Human mode: plain text on stdout, errors on stderr, no JSON.
	human := e.runIn(e.cwd, "", []string{"TASKS_OUTPUT="}, "init", "--name", "h")
	if human.code != 0 || human.env != nil || !strings.Contains(human.stdout, "registered project proj-") {
		t.Fatalf("human init: %+v", human)
	}
	herr := e.runIn(e.cwd, "", []string{"TASKS_OUTPUT="}, "show", "task-0000000000000000")
	if herr.code != 1 || herr.stdout != "" || !strings.Contains(herr.stderr, "error [NOT_FOUND]") {
		t.Fatalf("human error: %+v", herr)
	}
	// --json flag overrides an unset environment.
	flagged := e.runIn(e.cwd, "", []string{"TASKS_OUTPUT="}, "--json", "projects")
	if flagged.env == nil || flagged.env["ok"] != true {
		t.Fatalf("--json: %+v", flagged)
	}
	// Usage errors exit 2 and still produce an envelope in JSON mode.
	u := e.run("show")
	if u.code != 2 || u.env["ok"] != false {
		t.Fatalf("usage: %+v", u)
	}
	if r := e.run("nope"); r.code != 2 {
		t.Fatalf("unknown command: %+v", r)
	}
	if r := e.run(); r.code != 2 {
		t.Fatalf("no command: %+v", r)
	}
	if r := e.run("show", "not-an-id"); r.code != 1 || r.env["error"].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("bad id: %+v", r)
	}
	// Domain errors exit 1.
	e.fails("NOT_FOUND", "show", "task-0000000000000000")
	// No project registered for a directory.
	other := t.TempDir()
	if r := e.runIn(other, "", nil, "list"); r.code != 1 || r.env["error"].(map[string]any)["code"] != "NO_PROJECT" {
		t.Fatalf("no project: %+v", r)
	}
	// Explicit selection by name works from anywhere.
	if r := e.runIn(other, "", nil, "--project", "h", "list"); r.code != 0 {
		t.Fatalf("by name: %+v", r)
	}
}

func TestLeaseExpiryAndResumeAcrossProcesses(t *testing.T) {
	e := newEnv(t)
	e.ok("init", "--name", "x")
	a := e.ok("add", "Reject expired access tokens")["id"].(string)
	s1 := e.ok("take", a, "--lease", "1s")
	tok1 := s1["token"].(string)
	e.ok("log", tok1, "--done", "half", "--next", "finish the other half")
	// The first "agent" process is gone; its lease ages out.
	time.Sleep(1100 * time.Millisecond)
	e.fails("LEASE_EXPIRED", "log", tok1, "--done", "too late")
	e.fails("LEASE_EXPIRED", "finish", tok1)
	st := e.ok("show", a)
	if st["status"] != "interrupted" {
		t.Fatalf("status: %v", st["status"])
	}
	s2 := e.ok("take")
	if s2["task_id"] != a || s2["resumed"] != true || s2["attempt"].(float64) != 2 {
		t.Fatalf("resume: %v", s2)
	}
	if s2["handoff"].(map[string]any)["latest_next"] != "finish the other half" {
		t.Fatalf("handoff lost: %v", s2["handoff"])
	}
	e.fails("SESSION_SUPERSEDED", "log", tok1, "--done", "zombie")
	e.ok("log", s2["token"].(string), "--done", "resumed")
	who := e.ok("whoami", s2["token"].(string))
	if who["status"] != "in_progress" {
		t.Fatalf("whoami: %v", who)
	}
}

func TestConcurrentProcessesClaimOnce(t *testing.T) {
	e := newEnv(t)
	e.ok("init", "--name", "c")
	a := e.ok("add", "contended")["id"].(string)
	const n = 24
	var wg sync.WaitGroup
	results := make([]result, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = e.run("take", a)
		}(i)
	}
	wg.Wait()
	wins := 0
	for _, r := range results {
		switch {
		case r.code == 0:
			wins++
		case r.env != nil && r.env["error"].(map[string]any)["code"] == "TASK_ALREADY_TAKEN":
		default:
			t.Errorf("unexpected result: code=%d %s %s", r.code, r.stdout, r.stderr)
		}
	}
	if wins != 1 {
		t.Fatalf("wins = %d", wins)
	}

	// Automatic selection across processes hands each task to one process.
	const m = 12
	ids := map[string]bool{}
	for i := 0; i < m; i++ {
		ids[e.ok("add", "work")["id"].(string)] = true
	}
	got := make([]result, m+4)
	var wg2 sync.WaitGroup
	for i := range got {
		wg2.Add(1)
		go func(i int) { defer wg2.Done(); got[i] = e.run("take") }(i)
	}
	wg2.Wait()
	seen := map[string]int{}
	none := 0
	for _, r := range got {
		if r.code == 0 {
			seen[r.env["result"].(map[string]any)["task_id"].(string)]++
		} else if r.env["error"].(map[string]any)["code"] == "NO_AVAILABLE_TASK" {
			none++
		} else {
			t.Errorf("unexpected: %s %s", r.stdout, r.stderr)
		}
	}
	if len(seen) != m || none != 4 {
		t.Fatalf("seen=%d none=%d", len(seen), none)
	}
	for id, c := range seen {
		if c != 1 || !ids[id] {
			t.Fatalf("task %s claimed %d times", id, c)
		}
	}
}

func TestCommandsWorkFromLinkedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	e := newEnv(t)
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(e.cwd, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(e.cwd, "f"), []byte("x"), 0o644)
	git(e.cwd, "add", "f")
	git(e.cwd, "commit", "-q", "-m", "init")
	sub := filepath.Join(e.cwd, "pkg", "deep")
	os.MkdirAll(sub, 0o755)
	linked := filepath.Join(filepath.Dir(e.cwd), "repo.task-x")
	git(e.cwd, "worktree", "add", "-q", "-b", "task-x", linked)

	proj := e.runIn(sub, "", nil, "init") // registered from a subdirectory
	if proj.code != 0 {
		t.Fatalf("init: %s %s", proj.stdout, proj.stderr)
	}
	root := proj.env["result"].(map[string]any)["root_path"].(string)
	real, _ := filepath.EvalSymlinks(e.cwd)
	if root != real {
		t.Fatalf("root %q != %q", root, real)
	}
	a := e.ok("add", "from main")["id"].(string)
	r := e.runIn(linked, "", nil, "list", "--available")
	if r.code != 0 || !strings.Contains(r.stdout, a) {
		t.Fatalf("list from linked worktree: %s %s", r.stdout, r.stderr)
	}
	if r := e.runIn(linked, "", nil, "take"); r.code != 0 || r.env["result"].(map[string]any)["task_id"] != a {
		t.Fatalf("take from linked worktree: %s %s", r.stdout, r.stderr)
	}
}

func TestJSONOutputIsStable(t *testing.T) {
	e := newEnv(t)
	e.ok("init", "--name", "s")
	a := e.ok("add", "A")["id"].(string)
	b := e.ok("add", "B")["id"].(string)
	e.ok("deps", "add", b, "--requires", a)
	first := e.run("graph", "--format", "json").stdout
	second := e.run("graph", "--format", "json").stdout
	if first != second {
		t.Fatalf("graph output differs between identical runs:\n%s\n%s", first, second)
	}
	var g struct {
		Result struct {
			Nodes []map[string]any `json:"nodes"`
			Edges []map[string]any `json:"edges"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(first), &g); err != nil || len(g.Result.Edges) != 1 || g.Result.Edges[0]["task"] != b || g.Result.Edges[0]["requires"] != a {
		t.Fatalf("graph json: %v %s", err, first)
	}
	empty := e.run("deps", "list", a)
	if !strings.Contains(empty.stdout, `"requires": []`) {
		t.Fatalf("empty lists must be [] not null: %s", empty.stdout)
	}
}
