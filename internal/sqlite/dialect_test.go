package sqlite

import (
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/must"
)

func TestRebindPlaceholders(t *testing.T) {
	got := rebindPlaceholders(`SELECT * FROM tasks WHERE id = ? AND kind = ?`)
	want := `SELECT * FROM tasks WHERE id = $1 AND kind = $2`
	if got != want {
		t.Fatalf("rebind: got %q, want %q", got, want)
	}
	if strings.Contains(rebindPlaceholders(want), "?") {
		t.Fatalf("rebind must be idempotent: %q", got)
	}
}

func TestIsPostgresURL(t *testing.T) {
	for _, path := range []string{"postgres://u:p@h:5432/atask", "postgresql://h/atask"} {
		if !IsPostgresURL(path) {
			t.Fatalf("%q must be a postgres URL", path)
		}
	}
	for _, path := range []string{"at.db", "/tmp/at.db", ":memory:", "sqlite:///tmp/at.db"} {
		if IsPostgresURL(path) {
			t.Fatalf("%q must not be a postgres URL", path)
		}
	}
}

func TestRedactedNeverCarriesCredentials(t *testing.T) {
	got := Redacted("postgres://user:secret@ep-1.example.com:5432/atask?sslmode=require")
	want := "postgres://ep-1.example.com:5432/atask"
	if got != want {
		t.Fatalf("redacted: got %q, want %q", got, want)
	}
	if strings.Contains(got, "secret") {
		t.Fatalf("redacted URL carries credentials: %q", got)
	}
	// A plain path is printed as is: it carries no credentials to strip.
	if got := Redacted("/tmp/at.db"); got != "/tmp/at.db" {
		t.Fatalf("a path must redact to itself: %q", got)
	}
}

func TestStorePathIsRedactedForPostgres(t *testing.T) {
	if IsPostgresURL(":memory:") {
		t.Fatal("in-memory must not read as postgres")
	}
	s, err := Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must.OK(t, s.Close()) }()
	if s.IsPostgres() {
		t.Fatal("a file store must not report itself postgres")
	}
	if got := s.Path(); got != ":memory:" {
		t.Fatalf("path: got %q, want %q", got, ":memory:")
	}
}
