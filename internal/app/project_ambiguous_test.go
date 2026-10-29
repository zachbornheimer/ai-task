package app_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
)

// A --project name shared by several projects must fail with
// INVALID_INPUT naming every match by id, and each listed id must resolve on
// retry. project add refuses a second name that collapses to the same key,
// but a board created before that rule can still hold the collision, so it
// is built directly here.
func TestResolveProjectAmbiguousNameListsMatches(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "at.db")
	e, err := app.Open(ctx, app.Config{Path: path, Actor: "zach", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })

	left, err := e.AddProject(ctx, "Case Fold")
	if err != nil {
		t.Fatal(err)
	}
	right, err := e.AddProject(ctx, "Placeholder")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.Exec(`UPDATE projects SET name = 'case-fold' WHERE id = ?`, string(right.ID)); err != nil {
		t.Fatal(err)
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = e.ResolveProject(ctx, "casefold")
	if !fault.Is(err, fault.CodeInvalidInput) {
		t.Fatalf("want INVALID_INPUT, got %v", err)
	}
	msg := err.Error()
	for _, p := range []struct{ id, name string }{{string(left.ID), left.Name}, {string(right.ID), "case-fold"}} {
		if want := p.id + " (" + p.name + ")"; !strings.Contains(msg, want) {
			t.Fatalf("error must list %q: %s", want, msg)
		}
		got, err := e.ResolveProject(ctx, p.id)
		if err != nil || string(got.ID) != p.id {
			t.Fatalf("retry with listed id %s: %v %+v", p.id, err, got)
		}
	}
}
