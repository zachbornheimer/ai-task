package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/must"
)

// TestBlankProjectBecomesGeneral: the pre-0018 default project (id ”) is
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
	exec(`DELETE FROM schema_migrations WHERE version = 18`)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must.OK(t, s.Close()) }()
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
