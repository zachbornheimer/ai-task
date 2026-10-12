package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestGuardEditCoversNewDirsAndNotebooks: the edit guard decides from the
// nearest existing ancestor of the target path, so creating a file in a
// directory that does not exist yet is still denied on the target branch,
// and NotebookEdit's notebook_path is guarded like file_path. Worktrees and
// paths outside any project stay allowed.
func TestGuardEditCoversNewDirsAndNotebooks(t *testing.T) {
	e := gitEnv(t)
	guard := func(cwd, tool, key, path string) result {
		t.Helper()
		return e.runIn(cwd, `{"tool_name":"`+tool+`","tool_input":{"`+key+`":"`+path+`"},"cwd":"`+cwd+`"}`, nil, "hook", "guard-edit")
	}
	denied := func(r result) bool {
		return r.code == 0 && strings.Contains(r.stdout, `"permissionDecision":"deny"`)
	}
	allowed := func(r result) bool { return r.code == 0 && strings.TrimSpace(r.stdout) == "" }

	if r := guard(e.cwd, "Write", "file_path", filepath.Join(e.cwd, "newdir", "deeper", "x.txt")); !denied(r) {
		t.Fatalf("a new file in a not-yet-existing directory must be denied: %d %s %s", r.code, r.stdout, r.stderr)
	}
	if r := guard(e.cwd, "Write", "file_path", filepath.Join("newdir", "x.txt")); !denied(r) {
		t.Fatalf("a relative path into a new directory must be denied: %d %s %s", r.code, r.stdout, r.stderr)
	}
	if r := guard(e.cwd, "NotebookEdit", "notebook_path", filepath.Join(e.cwd, "analysis.ipynb")); !denied(r) {
		t.Fatalf("NotebookEdit on the target branch must be denied: %d %s %s", r.code, r.stdout, r.stderr)
	}
	if r := guard(e.cwd, "NotebookEdit", "notebook_path", filepath.Join(e.cwd, "nb", "analysis.ipynb")); !denied(r) {
		t.Fatalf("NotebookEdit in a new directory must be denied: %d %s %s", r.code, r.stdout, r.stderr)
	}
	// Outside any project, new directories and notebooks are left alone.
	out := t.TempDir()
	if r := guard(e.cwd, "Write", "file_path", filepath.Join(out, "new", "x.txt")); !allowed(r) {
		t.Fatalf("a new directory outside any project must be allowed: %d %s %s", r.code, r.stdout, r.stderr)
	}
	if r := guard(e.cwd, "NotebookEdit", "notebook_path", filepath.Join(out, "n.ipynb")); !allowed(r) {
		t.Fatalf("a notebook outside any project must be allowed: %d %s %s", r.code, r.stdout, r.stderr)
	}
	// Inside a task worktree both are allowed, new directory or not.
	e.ok("add", "Guarded", "--check", "u: test -f g.txt", "--regression-check", "r: true")
	ws := e.ok("claim")["workspace"].(string)
	if r := guard(ws, "Write", "file_path", filepath.Join(ws, "newdir", "x.txt")); !allowed(r) {
		t.Fatalf("a new directory in a task worktree must be allowed: %d %s %s", r.code, r.stdout, r.stderr)
	}
	if r := guard(ws, "NotebookEdit", "notebook_path", filepath.Join(ws, "n.ipynb")); !allowed(r) {
		t.Fatalf("a notebook in a task worktree must be allowed: %d %s %s", r.code, r.stdout, r.stderr)
	}
}
