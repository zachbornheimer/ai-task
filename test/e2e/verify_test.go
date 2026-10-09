package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitEnv makes the env's working directory a Git repository with one commit.
func gitEnv(t *testing.T) *env {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	e := newEnv(t)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", e.cwd}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(e.cwd, "f.txt"), []byte("one"), 0o644)
	git("add", "f.txt")
	git("commit", "-q", "-m", "one")
	e.ok("init", "--name", "g")
	return e
}

func TestFinishRunsChecksAndRecordsEvidence(t *testing.T) {
	e := gitEnv(t)
	a := e.ok("add", "passes", "--check", "unit: sh -c 'echo ok'")["id"].(string)
	sa := e.ok("take", a)
	fin := e.ok("finish", sa["token"].(string))
	if fin["status"] != "complete" || fin["verification"] != "passed" || len(fin["revision"].(string)) != 40 {
		t.Fatalf("finish: %v", fin)
	}
	b := e.ok("add", "fails", "--check", "unit: sh -c 'echo nope >&2; exit 7'", "--optional-check", "lint: true")["id"].(string)
	sb := e.ok("take", b)
	r := e.fails("VERIFICATION_FAILED", "finish", sb["token"].(string))
	details := r.env["error"].(map[string]any)["details"].(map[string]any)
	if details["status"] != "verification_failed" || details["run_id"].(float64) == 0 {
		t.Fatalf("details: %v", details)
	}
	ev := e.ok("evidence", b)
	runs := ev["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs: %v", runs)
	}
	rows := runs[0].(map[string]any)["evidence"].([]any)
	if len(rows) != 2 || rows[0].(map[string]any)["outcome"] != "failed" || rows[0].(map[string]any)["exit_code"].(float64) != 7 || !strings.Contains(rows[0].(map[string]any)["stderr"].(string), "nope") {
		t.Fatalf("evidence rows: %v", rows)
	}
	if rows[1].(map[string]any)["outcome"] != "passed" || rows[1].(map[string]any)["required"] != false {
		t.Fatalf("optional row: %v", rows[1])
	}
	// Human rendering of the failure goes to stderr with the evidence.
	h := e.runIn(e.cwd, "", []string{"TASKS_OUTPUT="}, "evidence", b)
	if h.code != 0 || !strings.Contains(h.stdout, "unit") || !strings.Contains(h.stdout, "nope") {
		t.Fatalf("human evidence: %+v", h)
	}
	// Tokens never appear in evidence output.
	if strings.Contains(e.run("evidence", b).stdout, "sess-") {
		t.Fatal("token leaked")
	}
	// Nothing to verify after a pass; --again re-verifies with reuse.
	e.fails("NOTHING_TO_VERIFY", "verify", a)
	again := e.ok("verify", a, "--again")
	if again["status"] != "complete" || !again["evidence"].([]any)[0].(map[string]any)["reused"].(bool) {
		t.Fatalf("again: %v", again)
	}
}

func TestDeferredVerificationAndPolicyChange(t *testing.T) {
	e := gitEnv(t)
	a := e.ok("add", "later", "--check", "unit: true")["id"].(string)
	b := e.ok("add", "dep")["id"].(string)
	e.ok("deps", "add", b, "--requires", a)
	sa := e.ok("take", a)
	fin := e.ok("finish", sa["token"].(string), "--no-verify")
	if fin["status"] != "awaiting_verification" {
		t.Fatalf("%v", fin)
	}
	e.fails("TASK_BLOCKED", "take", b)
	e.fails("TASK_AWAITING_VERIFICATION", "take", a)
	v := e.ok("verify", a)
	if v["status"] != "complete" {
		t.Fatalf("verify: %v", v)
	}
	if e.ok("show", b)["status"] != "available" {
		t.Fatal("dependent not released after verification")
	}
	// Tightening the policy withdraws completion until re-verified.
	p := e.ok("policy", a, "--check", "unit: true", "--check", "more: sh -c 'exit 1'")
	if p["status"] != "awaiting_verification" {
		t.Fatalf("policy change: %v", p)
	}
	if e.ok("show", b)["status"] != "blocked" {
		t.Fatal("dependent must re-block when the prerequisite's completion is withdrawn")
	}
	r := e.fails("VERIFICATION_FAILED", "verify", a)
	if !strings.Contains(r.stdout, "more") {
		t.Fatalf("%s", r.stdout)
	}
	shown := e.ok("policy", a)
	var pol struct {
		TaskChecks []map[string]any `json:"task_checks"`
	}
	json.Unmarshal(mustJSON(t, shown["policy"]), &pol)
	if len(pol.TaskChecks) != 2 || pol.TaskChecks[1]["id"] != "more" {
		t.Fatalf("policy: %v", shown)
	}
	// Dirty tree blocks submission with an actionable code.
	c := e.ok("add", "dirty")["id"].(string)
	sc := e.ok("take", c)
	os.WriteFile(filepath.Join(e.cwd, "scratch.txt"), []byte("x"), 0o644)
	e.fails("WORKSPACE_DIRTY", "finish", sc["token"].(string))
	os.Remove(filepath.Join(e.cwd, "scratch.txt"))
	e.ok("finish", sc["token"].(string))
}

// TestChecksRunWithoutHoldingTheDatabase proves no write transaction is
// held while a check executes: the check itself invokes the CLI to write to
// the same database and must succeed well within the busy timeout.
func TestChecksRunWithoutHoldingTheDatabase(t *testing.T) {
	e := gitEnv(t)
	script := bin + " --db " + e.db + " --project g add from-inside-check"
	a := e.ok("add", "reentrant", "--check", "nested: "+script)["id"].(string)
	sa := e.ok("take", a)
	fin := e.ok("finish", sa["token"].(string))
	if fin["status"] != "complete" {
		t.Fatalf("%v", fin)
	}
	r := e.run("list", "--all")
	if !strings.Contains(r.stdout, "from-inside-check") {
		t.Fatalf("nested write did not land: %s", r.stdout)
	}
}
