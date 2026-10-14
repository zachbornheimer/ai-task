package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/testgit"
)

// doctorCheck returns the doctor finding with the given id, or nil.
func doctorCheck(rep map[string]any, id string) map[string]any {
	checks, _ := rep["checks"].([]any)
	for _, c := range checks {
		if m, _ := c.(map[string]any); m != nil && m["id"] == id {
			return m
		}
	}
	return nil
}

// TestInitAndDoctorWarnAboutUncommittedFiles: init in a directory that
// already holds files creates the repository without committing them and
// says they must be committed before the baseline can pass; doctor warns
// while the target checkout has uncommitted or untracked files and is
// quiet once they are committed.
func TestInitAndDoctorWarnAboutUncommittedFiles(t *testing.T) {
	e := newEnv(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	os.WriteFile(filepath.Join(e.cwd, "main.go"), []byte("package main\n"), 0o644)

	res := e.ok("init", "--name", "u")
	if res["created_repo"] != true {
		t.Fatalf("init did not create the repository: %v", res)
	}
	files, _ := res["untracked_files"].([]any)
	if len(files) != 1 || files[0] != "main.go" {
		t.Fatalf("init does not list the untracked files: %v", res)
	}
	warn, _ := res["warning"].(string)
	if !strings.Contains(warn, "untracked") || !strings.Contains(warn, "commit") || !strings.Contains(warn, "baseline") {
		t.Fatalf("init warning: %q", warn)
	}
	// init never commits them itself.
	if out := testgit.Run(t, e.cwd, "ls-files"); out != "" {
		t.Fatalf("init committed the user's files: %q", out)
	}
	// The human form says it too.
	h := newEnv(t)
	os.WriteFile(filepath.Join(h.cwd, "lib.go"), []byte("package lib\n"), 0o644)
	if r := h.runIn(h.cwd, "", []string{"ATASK_OUTPUT=text"}, "init"); !strings.Contains(r.stdout, "untracked") || !strings.Contains(r.stdout, "lib.go") {
		t.Fatalf("human init output: %s", r.stdout)
	}
	// An empty directory has nothing to warn about.
	if r := newEnv(t).run("init"); r.env["result"].(map[string]any)["warning"] != nil {
		t.Fatalf("init warned about nothing: %s", r.stdout)
	}

	// doctor warns about the untracked file, and about a modified one.
	e.git(e.cwd, "config", "user.name", "t")
	e.git(e.cwd, "config", "user.email", "t@t")
	rep := e.ok("doctor")
	ch := doctorCheck(rep, "uncommitted_files")
	if ch == nil || ch["ok"] != false || ch["severity"] != "warning" || !strings.Contains(ch["message"].(string), "main.go") {
		t.Fatalf("doctor on untracked files: %v", ch)
	}
	if rep["healthy"] != true {
		t.Fatalf("a warning must not make the project unhealthy: %v", rep)
	}
	e.git(e.cwd, "add", "main.go", "AGENTS.md", "CLAUDE.md", ".claude/settings.json")
	e.git(e.cwd, "commit", "-q", "-m", "code")
	if ch := doctorCheck(e.ok("doctor"), "uncommitted_files"); ch == nil || ch["ok"] != true {
		t.Fatalf("doctor on a clean checkout: %v", ch)
	}
	os.WriteFile(filepath.Join(e.cwd, "main.go"), []byte("package main\n// edited\n"), 0o644)
	if ch := doctorCheck(e.ok("doctor"), "uncommitted_files"); ch == nil || ch["ok"] != false || !strings.Contains(ch["message"].(string), "main.go") {
		t.Fatalf("doctor on a modified file: %v", ch)
	}
}
