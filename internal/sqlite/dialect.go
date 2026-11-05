package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// Package sqlite's queries are written in SQLite's SQL. A dialect owns
// everything that differs between storage engines: the placeholder marker,
// the Go encoding of stored boolean facts, and how an INSERT reports the
// row id it allocated. Schema shape, migrations, and semantics are shared;
// the engine difference stays in this one file.

// dialect adapts statements and arguments for one storage engine.
type dialect interface {
	// driver names the registered database/sql driver.
	driver() string
	// rebound rewrites SQLite's ? placeholders into the engine's marker.
	rebound(query string) string
	// adapt converts one Go argument for the engine's driver. The store
	// keeps SQLite's INTEGER 0/1 for boolean facts (required, reused,
	// passed, detach, timed_out, sandboxed), so only the driver encoding
	// differs, never the stored value.
	adapt(v any) any
	// insertID runs an INSERT and reports the row id it allocated:
	// SQLite reads back last_insert_rowid(), PostgreSQL appends
	// RETURNING id (pgx does not implement Result.LastInsertId).
	insertID(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, error)
	// isPostgres reports whether the engine is PostgreSQL.
	isPostgres() bool
	// txBounds is the statement that arms the engine's server-side wait
	// bounds for a transaction, run right after BEGIN; "" when the engine
	// needs none.
	txBounds() string
}

// --- SQLite ---

type sqliteDialect struct{}

func (sqliteDialect) driver() string          { return "sqlite" }
func (sqliteDialect) rebound(q string) string { return q }
func (sqliteDialect) adapt(v any) any         { return v }
func (sqliteDialect) isPostgres() bool        { return false }

// txBounds: a local file's waits are lock waits, already bounded by
// busy_timeout on the connection; a transaction adds nothing.
func (sqliteDialect) txBounds() string { return "" }

func (d sqliteDialect) insertID(_ context.Context, tx *sql.Tx, q string, args ...any) (int64, error) {
	res, err := tx.ExecContext(context.Background(), q, args...)
	if err != nil {
		return 0, err
	}
	// The driver fills LastInsertId for INSERTs of INTEGER PRIMARY KEY
	// columns; a failure to read it back leaves the id unknown (0).
	id, _ := res.LastInsertId()
	return id, nil
}

// --- PostgreSQL ---

// postgresDialect carries the transaction bounds parsed from the URL (the
// defaults when it names none), so the engine difference and its tuning
// travel as one value.
type postgresDialect struct {
	bounds string
}

func (postgresDialect) driver() string          { return "pgx" }
func (postgresDialect) isPostgres() bool        { return true }
func (postgresDialect) rebound(q string) string { return rebindPlaceholders(q) }
func (d postgresDialect) txBounds() string      { return d.bounds }
func (postgresDialect) adapt(v any) any {
	// The schema stores boolean facts as BIGINT 0/1 (SQLite's convention);
	// a Go bool must cross the wire as an integer for those columns.
	if b, ok := v.(bool); ok {
		return b2i(b)
	}
	// SQLite TEXT is byte-agnostic, so captured command output (evidence
	// stdout/stderr, run results) can carry bytes that are not valid
	// UTF-8; PostgreSQL TEXT refuses them (SQLSTATE 22021). A check's
	// captured output is display data, never parsed, so invalid bytes
	// become U+FFFD rather than failing the write. The local store keeps
	// the exact bytes.
	if s, ok := v.(string); ok {
		return strings.ToValidUTF8(s, string(utf8.RuneError))
	}
	return v
}

func (d postgresDialect) insertID(ctx context.Context, tx *sql.Tx, q string, args ...any) (int64, error) {
	// pgx does not implement Result.LastInsertId; RETURNING id is the
	// PostgreSQL way to report the allocated row id.
	var id int64
	if err := tx.QueryRowContext(ctx, q+" RETURNING id", args...).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// rebindPlaceholders rewrites each ? marker into $1, $2, ... in order. No
// query in this package puts a ? inside a string literal, so a positional
// scan is exact.
func rebindPlaceholders(q string) string {
	var b strings.Builder
	n := 0
	for i := range len(q) {
		if q[i] != '?' {
			b.WriteByte(q[i])
			continue
		}
		n++
		b.WriteByte('$')
		b.WriteString(strconv.Itoa(n))
	}
	return b.String()
}

// pgSchemes are the URL schemes that select the PostgreSQL engine.
var pgSchemes = []string{"postgres://", "postgresql://"}

// IsPostgresURL reports whether path names a PostgreSQL database by URL
// (postgres:// or postgresql://) rather than a SQLite file path.
func IsPostgresURL(path string) bool {
	for _, s := range pgSchemes {
		if strings.HasPrefix(path, s) {
			return true
		}
	}
	return false
}

// Redacted returns a printable form of a database path: a PostgreSQL URL
// keeps only its scheme, host, and database name, so a connection string's
// credentials never reach an error message or a report.
func Redacted(path string) string {
	if !IsPostgresURL(path) {
		return path
	}
	u, err := url.Parse(path)
	if err != nil {
		return "postgres://<unparseable URL>"
	}
	return u.Scheme + "://" + u.Host + u.Path
}

// sqlitePath strips the sqlite:// URL scheme, if any, from a database path.
func sqlitePath(path string) string {
	return strings.TrimPrefix(path, "sqlite://")
}

// Remote wait bounds: a remote store must fail loudly, never hang. A
// connection attempt or a statement that exceeds its bound reports the
// timeout as an error; a client that stalls inside a transaction is cut
// loose by the server instead of holding one open forever.
const (
	// pgConnectTimeout bounds one connection attempt. It is enforced by
	// the client's own dialer, so it holds whatever the URL names.
	pgConnectTimeout = 10 * time.Second
	// pgStatementTimeout bounds one statement, however slow the network.
	pgStatementTimeout = 30 * time.Second
	// pgIdleInTxTimeout bounds how long a server session may sit in an
	// open transaction without a client command.
	pgIdleInTxTimeout = 120 * time.Second
)

// pgBoundsStatement returns the statement that arms one transaction's
// wait bounds: the widths the URL named (pgx keeps parameters it does not
// recognize in RuntimeParams) and the defaults for those it did not.
// Transaction scope is required, not merely tidy: a pooler in front of the
// database (Neon's, for one) does not carry startup parameters through—a
// statement_timeout named there stays at its default, and a session
// default could not be trusted anyway, since the next client of the same
// pooled connection would inherit it.
func pgBoundsStatement(params map[string]string) string {
	return fmt.Sprintf(
		`SELECT set_config('statement_timeout', '%d', true), set_config('idle_in_transaction_session_timeout', '%d', true)`,
		boundWidth(params, "statement_timeout", pgStatementTimeout).Milliseconds(),
		boundWidth(params, "idle_in_transaction_session_timeout", pgIdleInTxTimeout).Milliseconds())
}

// boundWidth reads one wait bound out of a URL's parameters: a bare
// integer is milliseconds, which is PostgreSQL's own unit for these
// settings, and a duration string ("2s") is read as such. Anything else,
// including a value that would not bound the wait at all, keeps the
// default rather than leaving the wait unbounded.
func boundWidth(params map[string]string, name string, def time.Duration) time.Duration {
	v := params[name]
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
		return time.Duration(n) * time.Millisecond
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	return def
}

// postgresConfig parses a PostgreSQL URL into the connection every remote
// store uses: the URL's own settings plus the client-side dial bound. The
// server-side bounds are armed per transaction (see pgBoundsStatement).
func postgresConfig(path string) (*pgx.ConnConfig, error) {
	config, err := pgx.ParseConfig(path)
	if err != nil {
		return nil, fault.WrapInternal(err, "parse postgres URL")
	}
	if config.ConnectTimeout == 0 {
		config.ConnectTimeout = pgConnectTimeout
	}
	return config, nil
}

// openPostgres opens a database/sql pool on a PostgreSQL URL and reports
// the dialect its transactions run under. The URL is validated up front so
// a malformed one fails as a parse error, not a lazy connect failure deep
// inside the first query; the parsed config becomes every pooled
// connection.
func openPostgres(path string) (*sql.DB, dialect, error) {
	config, err := postgresConfig(path)
	if err != nil {
		return nil, nil, err
	}
	return stdlib.OpenDB(*config), postgresDialect{bounds: pgBoundsStatement(config.RuntimeParams)}, nil
}

// rebindTx adapts every statement of a transaction to the store's engine.
// Queries stay written in SQLite's SQL throughout the package; this wrapper
// rewrites the placeholders and converts arguments on their way through.
type rebindTx struct {
	tx *sql.Tx
	d  dialect
}

func (r rebindTx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return r.tx.ExecContext(ctx, r.d.rebound(q), r.adaptArgs(args)...)
}

func (r rebindTx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return r.tx.QueryContext(ctx, r.d.rebound(q), r.adaptArgs(args)...)
}

func (r rebindTx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return r.tx.QueryRowContext(ctx, r.d.rebound(q), r.adaptArgs(args)...)
}

// insertID runs an INSERT and reports the row id it allocated.
func (r rebindTx) insertID(ctx context.Context, q string, args ...any) (int64, error) {
	return r.d.insertID(ctx, r.tx, r.d.rebound(q), r.adaptArgs(args)...)
}

func (r rebindTx) adaptArgs(args []any) []any {
	if !r.d.isPostgres() {
		return args
	}
	for i, v := range args {
		args[i] = r.d.adapt(v)
	}
	return args
}

// insertID runs an INSERT inside this transaction and reports the row id
// it allocated, for the transaction's engine.
func (t *Tx) insertID(q string, args ...any) (int64, error) {
	return t.tx.insertID(t.ctx, q, args...)
}
