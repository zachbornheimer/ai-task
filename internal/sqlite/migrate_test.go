package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

// TestGroupsBeforeSizesBecomeLarge: groups planned before migration 0011
// carry the column default 'small'; migration 0012 raises those that have
// never been verified to 'large', and leaves tasks, groups that already
// have a group-mode run, and groups of another size alone.
func TestGroupsBeforeSizesBecomeLarge(t *testing.T) {
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
	exec(`INSERT INTO projects (id, name, created_at, updated_at) VALUES ('p', 'p', 1, 1)`)
	task := func(id, kind, size string) {
		exec(`INSERT INTO tasks (id, project_id, description, outcome, policy_digest, kind, size, created_at, updated_at) VALUES (?, 'p', ?, ?, 'd', ?, ?, 1, 1)`, id, id, id, kind, size)
	}
	task("old-group", "group", "small")
	task("verified-group", "group", "small")
	task("medium-group", "group", "medium")
	task("plain-task", "task", "small")
	exec(`INSERT INTO verification_runs (task_id, mode, status, policy_digest, created_at) VALUES ('verified-group', 'group', 'passed', 'd', 1)`)
	exec(`INSERT INTO verification_runs (task_id, mode, status, policy_digest, created_at) VALUES ('old-group', 'task', 'passed', 'd', 1)`)

	// Rewind to a database that has not seen 0012 yet, then reopen it.
	exec(`DELETE FROM schema_migrations WHERE version = 12`)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	want := map[string]string{
		"old-group":      "large",
		"verified-group": "small",
		"medium-group":   "medium",
		"plain-task":     "small",
	}
	for id, size := range want {
		var got string
		if err := s.db.QueryRowContext(ctx, `SELECT size FROM tasks WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != size {
			t.Errorf("%s: size = %q, want %q", id, got, size)
		}
	}
}

// TestBlankProjectBecomesGeneral: the pre-0015 default project (id ”) is
// renamed to 'general' with a real id, and its tasks move with it.
func TestBlankProjectBecomesGeneral(t *testing.T) {
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
	exec(`INSERT INTO projects (id, name, created_at, updated_at) VALUES ('', '', 1, 1)`)
	exec(`INSERT INTO tasks (id, project_id, description, outcome, policy_digest, kind, created_at, updated_at) VALUES ('t1', '', 't1', 't1', 'd', 'task', 1, 1)`)
	exec(`DELETE FROM schema_migrations WHERE version = 15`)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var id, name, taskProject string
	if err := s.db.QueryRowContext(ctx, `SELECT id, name FROM projects`).Scan(&id, &name); err != nil {
		t.Fatalf("exactly one project expected: %v", err)
	}
	if name != "general" || id == "" {
		t.Fatalf("want general with real id, got id=%q name=%q", id, name)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT project_id FROM tasks WHERE id = 't1'`).Scan(&taskProject); err != nil || taskProject != id {
		t.Fatalf("task must follow the project: %q %v", taskProject, err)
	}
}
