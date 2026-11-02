package sqlite

import (
	"strings"
	"testing"
	"time"
)

// TestPostgresDialBound: one connection attempt is bounded, and the URL's
// own connect_timeout still wins.
func TestPostgresDialBound(t *testing.T) {
	cfg, err := postgresConfig("postgres://user:pw@127.0.0.1:5432/atask")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnectTimeout != pgConnectTimeout {
		t.Fatalf("dial bound: got %s, want %s", cfg.ConnectTimeout, pgConnectTimeout)
	}
	cfg, err = postgresConfig("postgres://user:pw@127.0.0.1:5432/atask?connect_timeout=3")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnectTimeout != 3*time.Second {
		t.Fatalf("dial bound from the URL: got %s, want 3s", cfg.ConnectTimeout)
	}
}

// TestTransactionBounds: SQLite arms none—its waits are lock waits already
// bounded by busy_timeout—and PostgreSQL arms both server-side waits for
// one transaction, with the widths the URL named or the defaults. The
// widths are read from the URL's runtime parameters because that is where
// a caller can state them.
func TestTransactionBounds(t *testing.T) {
	if got := (sqliteDialect{}).txBounds(); got != "" {
		t.Fatalf("sqlite must arm no transaction bound; got %q", got)
	}
	pg := func(query string) postgresDialect {
		cfg, err := postgresConfig("postgres://user:pw@127.0.0.1:5432/atask" + query)
		if err != nil {
			t.Fatal(err)
		}
		return postgresDialect{bounds: pgBoundsStatement(cfg.RuntimeParams)}
	}
	for _, tc := range []struct {
		name  string
		query string
		want  []string
	}{
		{
			name: "defaults",
			want: []string{"set_config('statement_timeout', '30000', true)", "set_config('idle_in_transaction_session_timeout', '120000', true)"},
		},
		{
			name:  "URL milliseconds",
			query: "?statement_timeout=250&idle_in_transaction_session_timeout=900",
			want:  []string{"set_config('statement_timeout', '250', true)", "set_config('idle_in_transaction_session_timeout', '900', true)"},
		},
		{
			name:  "URL duration string",
			query: "?statement_timeout=2s",
			want:  []string{"set_config('statement_timeout', '2000', true)", "set_config('idle_in_transaction_session_timeout', '120000', true)"},
		},
		{
			// A width that cannot bound a wait would silently unbound every
			// remote statement: the default stays.
			name:  "unusable widths keep the default",
			query: "?statement_timeout=0&idle_in_transaction_session_timeout=soon",
			want:  []string{"set_config('statement_timeout', '30000', true)", "set_config('idle_in_transaction_session_timeout', '120000', true)"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pg(tc.query).txBounds()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("transaction bound %q does not name %q", got, want)
				}
			}
			if !strings.Contains(got, ", true)") {
				t.Fatalf("transaction bound %q is not transaction-scoped", got)
			}
		})
	}
}
