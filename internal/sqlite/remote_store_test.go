package sqlite

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/must"
)

// remoteURLEnv names the PostgreSQL store the remote tests work against.
// They are skipped unless it is set, so the suite runs everywhere; the
// defect they cover cannot be reproduced on a local file, where the same
// round trips cost microseconds.
const remoteURLEnv = "ATASK_TEST_POSTGRES_URL"

// remoteURL returns the remote store's URL, skipping the test when the
// environment names none. The two schemes spelled out—the same pair
// IsPostgresURL accepts—keep the value from naming anything else: a bare
// path from the environment would open a local file and make these
// assertions meaningless.
func remoteURL(t *testing.T) string {
	t.Helper()
	raw := os.Getenv(remoteURLEnv)
	if raw == "" {
		t.Skipf("set %s to a reachable PostgreSQL URL to run the remote-store tests", remoteURLEnv)
	}
	if !strings.HasPrefix(raw, "postgres://") && !strings.HasPrefix(raw, "postgresql://") {
		t.Fatalf("%s must be a postgres:// or postgresql:// URL; got %q", remoteURLEnv, Redacted(raw))
	}
	return raw
}

// withParam returns raw with one URL parameter set, whatever query string
// it already carries.
func withParam(t *testing.T, raw, name, value string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", remoteURLEnv, err)
	}
	q := u.Query()
	q.Set(name, value)
	u.RawQuery = q.Encode()
	return u.String()
}

// openRemoteStore opens the store the remote tests work against through
// the same connection path Open uses. The URL comes from the environment,
// so it goes only into openPostgres, which dials the named server and can
// never open a local file; the store is built directly rather than through
// Open, whose path argument may name a SQLite file.
func openRemoteStore(t *testing.T, raw string) *Store {
	t.Helper()
	db, d, err := openPostgres(raw)
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db, path: Redacted(raw), d: d}
	t.Cleanup(func() { must.OK(t, s.Close()) })
	if err := s.migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestRemoteStatementWaitIsBounded proves a stalled statement becomes a
// loud error that names the timeout, instead of hanging: the transaction
// bound reaches the server through whatever pooler the URL names, and a
// statement past its width is canceled by the server.
func TestRemoteStatementWaitIsBounded(t *testing.T) {
	ctx := context.Background()
	// A 500ms bound is far above the migrations and ledger reads Open
	// performs, so the stall below is what trips it.
	s := openRemoteStore(t, withParam(t, remoteURL(t), "statement_timeout", "500"))
	start := time.Now()
	err := s.Read(ctx, func(tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx, "select pg_sleep(5)")
		return err
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a 5s statement under a 500ms bound must fail")
	}
	if !strings.Contains(err.Error(), "statement timeout") {
		t.Fatalf("the failure does not name the statement timeout: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the bound did not apply: the statement ran for %s before failing", elapsed)
	}
}

// TestRemoteStalledClientLosesItsTransaction proves the transaction is
// abandoned server-side when the client stops driving it: after the
// idle-in-transaction width, the server ends the transaction and the next
// statement says so, so a wedged client cannot hold locks forever.
func TestRemoteStalledClientLosesItsTransaction(t *testing.T) {
	ctx := context.Background()
	s := openRemoteStore(t, withParam(t, remoteURL(t), "idle_in_transaction_session_timeout", "1s"))
	err := s.Read(ctx, func(tx *Tx) error {
		var one int
		if err := tx.tx.QueryRowContext(ctx, "select 1").Scan(&one); err != nil {
			return err
		}
		// The client goes away without ending the transaction.
		time.Sleep(2500 * time.Millisecond)
		return tx.tx.QueryRowContext(ctx, "select 1").Scan(&one)
	})
	if err == nil {
		t.Fatal("a transaction idle past its width must be abandoned by the server")
	}
	if !strings.Contains(err.Error(), "idle-in-transaction timeout") {
		t.Fatalf("the failure does not name the idle-in-transaction timeout: %v", err)
	}
}
