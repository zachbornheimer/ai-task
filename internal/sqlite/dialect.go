package sqlite

import (
	"context"
	"database/sql"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

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
}

// --- SQLite ---

type sqliteDialect struct{}

func (sqliteDialect) driver() string          { return "sqlite" }
func (sqliteDialect) rebound(q string) string { return q }
func (sqliteDialect) adapt(v any) any         { return v }
func (sqliteDialect) isPostgres() bool        { return false }
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

type postgresDialect struct{}

func (postgresDialect) driver() string          { return "pgx" }
func (postgresDialect) isPostgres() bool        { return true }
func (postgresDialect) rebound(q string) string { return rebindPlaceholders(q) }
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

// openPostgres opens a database/sql pool on a PostgreSQL URL. The URL is
// validated up front so a malformed one fails as a parse error, not a lazy
// connect failure deep inside the first query.
func openPostgres(path string) (*sql.DB, error) {
	if _, err := pgx.ParseConfig(path); err != nil {
		return nil, fault.WrapInternal(err, "parse postgres URL")
	}
	db, err := sql.Open("pgx", path)
	if err != nil {
		return nil, fault.WrapInternal(err, "open postgres")
	}
	return db, nil
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
