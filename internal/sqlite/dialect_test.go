package sqlite

import (
	"strconv"
	"strings"
	"testing"
	"time"

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

// TestScopeClaimableBindsOneArgumentPerPlaceholder pins the invariant every
// engine-specific fragment depends on: a ? in a statement is a placeholder
// and nothing else. An engine fragment that spells an operator ? breaks it —
// PostgreSQL's jsonb ? did, on every board, as `select candidate: syntax
// error at or near "$5"`, because the rebind pass turned the operator into a
// placeholder no argument filled. A fragment that reintroduces one fails
// here instead of at the first claim.
func TestScopeClaimableBindsOneArgumentPerPlaceholder(t *testing.T) {
	for _, tc := range []struct {
		engine dialect
	}{
		{sqliteDialect{}},
		{postgresDialect{}},
	} {
		tx := &Tx{d: tc.engine}
		where, args := tx.scopeWhere("proj-1", ScopeClaimable, time.Now(), 3, false)
		stmt := tx.recordSelect() + where + ` ORDER BY t.created_at, t.id LIMIT 1`
		bound := tc.engine.rebound(stmt)
		if !tc.engine.isPostgres() {
			if got := strings.Count(bound, "?"); got != len(args) {
				t.Fatalf("%d ? placeholder(s) for %d bound argument(s):\n%s", got, len(args), bound)
			}
			continue
		}
		// Every ? the writer typed is an argument marker, so after rebinding
		// none may be left: the jsonb ? operator was, and the statement then
		// read as `$5` with five arguments bound.
		if got := strings.Count(bound, "?"); got != 0 {
			t.Fatalf("%d ? marker(s) survived rebinding:\n%s", got, bound)
		}
		if got := countPlaceholders(bound); got != len(args) {
			t.Fatalf("%d placeholder(s) for %d bound argument(s):\n%s", got, len(args), bound)
		}
		// Numbering must be 1..n with no gap or repeat: an operator ? shifted
		// every argument after it, which is the failure this test exists for.
		for n := 1; n <= len(args); n++ {
			if got := strings.Count(bound, "$"+strconv.Itoa(n)); got != 1 {
				t.Fatalf("placeholder $%d appears %d time(s), want 1:\n%s", n, got, bound)
			}
		}
	}
}

// countPlaceholders counts $n markers, ignoring a JSON path's leading $ (as
// in '$.proof') by requiring a digit after the dollar sign.
func countPlaceholders(q string) int {
	n := 0
	for i := 0; i+1 < len(q); i++ {
		if q[i] == '$' && q[i+1] >= '0' && q[i+1] <= '9' {
			n++
		}
	}
	return n
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
