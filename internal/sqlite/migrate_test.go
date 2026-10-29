package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/must"
)

// TestProjectsHoldNoRepository: a project is a planning scope and a task
// carries the execution binding. The final schema has no repository column
// on projects and both binding columns on tasks.
func TestProjectsHoldNoRepository(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "at.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must.OK(t, s.Close()) }()
	cols := func(table string) map[string]bool {
		t.Helper()
		rows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		defer discard.CloseReadOnly(rows)
		out := map[string]bool{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			out[name] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	projects := cols("projects")
	for _, gone := range []string{"root_path", "target_branch", "workspace_root", "integration"} {
		if projects[gone] {
			t.Fatalf("projects must not hold %s: %v", gone, projects)
		}
	}
	tasks := cols("tasks")
	for _, want := range []string{"repo_root", "base_ref"} {
		if !tasks[want] {
			t.Fatalf("tasks must hold %s: %v", want, tasks)
		}
	}
}

// TestMigrationLedgerRefusesNameMismatch: a ledger that records a different
// file name for an embedded migration number is refused, not skipped.
func TestMigrationLedgerRefusesNameMismatch(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "at.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var version int64
	var embedded string
	if err := s.db.QueryRowContext(ctx, `SELECT version, name FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version, &embedded); err != nil {
		t.Fatal(err)
	}
	const recorded = "0022_other.sql"
	if _, err := s.db.ExecContext(ctx, `UPDATE schema_migrations SET name = ? WHERE version = ?`, recorded, version); err != nil {
		t.Fatal(err)
	}
	must.OK(t, s.Close())
	_, err = Open(ctx, path)
	if err == nil {
		t.Fatal("Open succeeded despite a ledger name mismatch")
	}
	for _, want := range []string{recorded, embedded, path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if !fault.Is(err, fault.CodeInvalidInput) {
		t.Errorf("code = %q, want INVALID_INPUT", fault.CodeOf(err))
	}
}

// TestTaskProjectsMigrationKeepsHome: a task stored before task_projects
// becomes position 0 of its own project, so no home changes.
func TestTaskProjectsMigrationKeepsHome(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "at.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO projects (id, name, created_at, updated_at) VALUES ('p1', 'one', 1, 1)`)
	exec(`INSERT INTO tasks (id, project_id, description, outcome, policy_digest, kind, created_at, updated_at) VALUES ('t1', 'p1', 't1', 't1', 'd', 'task', 1, 1)`)
	exec(`DROP TABLE task_projects`)
	exec(`DELETE FROM schema_migrations WHERE name LIKE '%task_projects%'`)
	must.OK(t, s.Close())
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must.OK(t, s.Close()) }()
	var pid string
	var pos int
	if err := s.db.QueryRowContext(ctx, `SELECT project_id, position FROM task_projects WHERE task_id = 't1'`).Scan(&pid, &pos); err != nil || pid != "p1" || pos != 0 {
		t.Fatalf("want (p1, 0), got (%q, %d) %v", pid, pos, err)
	}
}
