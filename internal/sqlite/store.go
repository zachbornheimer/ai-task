// Package sqlite is the authoritative store. It knows the schema and SQL and
// nothing about what the facts mean: eligibility, authority, and completion
// are decided by the domain packages using the facts this package returns.
//
// The queries are written in SQLite's SQL. A store opened on a PostgreSQL
// URL (ATASK_DB_URL) runs the same queries through a dialect that rewrites
// the placeholders and the few engine-specific functions; see dialect.go
// and dialect_sql.go. Local SQLite files keep today's behavior exactly:
// same DSN pragmas, same migrations.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // PostgreSQL driver for ATASK_DB_URL
	_ "modernc.org/sqlite"             // pure-Go SQLite driver; see docs/decisions/0002-sqlite.md

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/fault"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

//go:embed postgres_schema.sql
var postgresSchema string

// pgSchemaVersion records the applied state of postgres_schema.sql in
// schema_migrations. SQLite migration files number from 1; this is far past
// them so the two ledgers never collide.
const pgSchemaVersion int64 = 9000000000

// BusyTimeout is how long a connection waits for a lock before failing. CLI
// invocations are short, so contention resolves in milliseconds in practice;
// the generous bound covers a slow verification-result commit.
const BusyTimeout = 10 * time.Second

// Store wraps one database: a SQLite file or a PostgreSQL URL.
type Store struct {
	db   *sql.DB
	path string
	d    dialect
}

// Open opens (creating if needed) the database at path and applies pending
// migrations. A PostgreSQL URL (postgres:// or postgresql://) opens the
// remote database and applies its schema; a path (or sqlite:// URL) opens a
// SQLite file. ":memory:" opens a private in-memory database for tests; it
// is limited to a single connection so every statement sees the same
// database.
func Open(ctx context.Context, path string) (*Store, error) {
	if IsPostgresURL(path) {
		db, err := openPostgres(path)
		if err != nil {
			return nil, err
		}
		s := &Store{db: db, path: path, d: postgresDialect{}}
		if err := s.migrate(ctx); err != nil {
			return nil, errors.Join(err, db.Close())
		}
		return s, nil
	}
	path = sqlitePath(path)
	memory := path == ":memory:"
	if !memory {
		path = filepath.Clean(path)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fault.WrapInternal(err, "create state directory")
		}
	}
	dsn := dsnFor(path, memory)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fault.WrapInternal(err, "open database")
	}
	if memory {
		db.SetMaxOpenConns(1)
	}
	s := &Store{db: db, path: path, d: sqliteDialect{}}
	if err := s.migrate(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return s, nil
}

func dsnFor(path string, memory bool) string {
	q := url.Values{}
	// Order matters for journal_mode: it must be set before any write.
	pragmas := []string{
		"busy_timeout(" + strconv.FormatInt(BusyTimeout.Milliseconds(), 10) + ")",
		"foreign_keys(1)",
		"synchronous(NORMAL)",
	}
	if memory {
		return "file::memory:?_pragma=foreign_keys(1)&_txlock=immediate"
	}
	pragmas = append([]string{"journal_mode(WAL)"}, pragmas...)
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	// Write transactions take the RESERVED lock up front so that two writers
	// never deadlock on a deferred-to-write upgrade; busy_timeout then
	// serializes them. Read-only transactions still begin deferred.
	q.Set("_txlock", "immediate")
	return "file:" + path + "?" + q.Encode()
}

// Path returns a printable form of the database path: the file path for
// SQLite, a credential-free URL for PostgreSQL. Callers that derive local
// state directories from it (sessions, logs, workspaces) branch on
// IsPostgresURL first.
func (s *Store) Path() string {
	if s.d.isPostgres() {
		return Redacted(s.path)
	}
	return s.path
}

// Close releases the connection pool.
func (s *Store) Close() error { return s.db.Close() }

// IsPostgres reports whether the store is a remote PostgreSQL database.
func (s *Store) IsPostgres() bool { return s.d.isPostgres() }

// Tx is a transaction handle exposing typed queries. All methods must be
// called before the enclosing Read/Write callback returns.
type Tx struct {
	tx  rebindTx
	ctx context.Context
	d   dialect
}

// Read runs fn in a read-only (deferred) transaction so that a multi-query
// read model is built from one consistent snapshot.
func (s *Store) Read(ctx context.Context, fn func(*Tx) error) error {
	return s.run(ctx, true, fn)
}

// Write runs fn in an immediate transaction. The callback must be short and
// must not run external processes; see docs/architecture.md.
func (s *Store) Write(ctx context.Context, fn func(*Tx) error) error {
	return s.run(ctx, false, fn)
}

func (s *Store) run(ctx context.Context, readOnly bool, fn func(*Tx) error) (err error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: readOnly})
	if err != nil {
		return fault.WrapInternal(err, "begin transaction")
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(&Tx{tx: rebindTx{tx: tx, d: s.d}, ctx: ctx, d: s.d}); err != nil {
		_ = tx.Rollback()
		if cerr := ctx.Err(); cerr != nil && fault.IsInternal(err) {
			return canceled(cerr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		// database/sql rolls a transaction back by itself when its context
		// ends; the commit then reports "already rolled back". That is the
		// caller's cancellation, not an internal fault: nothing was written.
		if cerr := ctx.Err(); cerr != nil {
			return canceled(cerr)
		}
		return fault.WrapInternal(err, "commit transaction")
	}
	return nil
}

// canceled reports a transaction abandoned because its context ended; it
// unwraps to the context error (context.Canceled or DeadlineExceeded).
func canceled(cerr error) error {
	return fault.Wrap(cerr, fault.CodeNotReady, "transaction abandoned: the caller's context ended (nothing was written)")
}

// migrate applies pending migrations: the embedded SQLite files for a file
// store, the final schema for a PostgreSQL store.
func (s *Store) migrate(ctx context.Context) error {
	if s.d.isPostgres() {
		return s.migratePostgres(ctx)
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return fault.WrapInternal(err, "read migrations")
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	// The migrations table itself is created idempotently outside the loop.
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
		return fault.WrapInternal(err, "create schema_migrations")
	}
	return s.Write(ctx, func(tx *Tx) error {
		applied := map[int64]string{}
		rows, err := tx.tx.QueryContext(ctx, `SELECT version, name FROM schema_migrations`)
		if err != nil {
			return fault.WrapInternal(err, "read schema_migrations")
		}
		for rows.Next() {
			var v int64
			var recorded string
			if err := rows.Scan(&v, &recorded); err != nil {
				discard.CloseReadOnly(rows)
				return err
			}
			applied[v] = recorded
		}
		discard.CloseReadOnly(rows)
		for _, name := range names {
			version, err := strconv.ParseInt(strings.SplitN(name, "_", 2)[0], 10, 64)
			if err != nil {
				return fmt.Errorf("migration %q: bad version prefix", name)
			}
			if recorded, done := applied[version]; done {
				if recorded != name {
					return fault.New(fault.CodeInvalidInput, "migration ledger in %s records version %d as %s but this build embeds %s; the database was migrated by a different build", s.path, version, recorded, name)
				}
				continue
			}
			body, err := migrationFS.ReadFile("migrations/" + name)
			if err != nil {
				return err
			}
			if _, err := tx.tx.ExecContext(ctx, string(body)); err != nil {
				return fault.WrapInternal(err, "apply migration %s", name)
			}
			if _, err := tx.tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`, version, name, time.Now().UnixMilli()); err != nil {
				return fault.WrapInternal(err, "record migration %s", name)
			}
		}
		return nil
	})
}

// migratePostgres applies the final PostgreSQL schema once and records it
// in schema_migrations. There are no incremental migrations to replay: the
// schema is only ever created from scratch by sync or first use, so a
// recorded version means it is current.
func (s *Store) migratePostgres(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version BIGINT PRIMARY KEY, name TEXT NOT NULL, applied_at BIGINT NOT NULL)`); err != nil {
		return fault.WrapInternal(err, "create schema_migrations")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fault.WrapInternal(err, "begin schema transaction")
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = $1`, pgSchemaVersion).Scan(&n); err != nil {
		_ = tx.Rollback()
		return fault.WrapInternal(err, "read schema_migrations")
	}
	if n == 0 {
		if _, err := tx.ExecContext(ctx, postgresSchema); err != nil {
			_ = tx.Rollback()
			return fault.WrapInternal(err, "apply postgres schema")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES ($1, $2, $3)`, pgSchemaVersion, "postgres_schema.sql", time.Now().UnixMilli()); err != nil {
			_ = tx.Rollback()
			return fault.WrapInternal(err, "record postgres schema")
		}
	}
	if err := tx.Commit(); err != nil {
		return fault.WrapInternal(err, "commit schema transaction")
	}
	return nil
}

// --- small conversion helpers shared by the query files ---

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

func nullMS(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMS(v.Int64)
	return &t
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

type (
	nullInt = sql.NullInt64
)

func wrapInternal(err error, what string) error {
	if err == nil {
		return nil
	}
	return fault.WrapInternal(err, "%s", what)
}

// IsUniqueViolation reports whether err is a UNIQUE/PRIMARY KEY conflict.
// Callers use it to retry random-ID generation on the (astronomically rare)
// collision instead of surfacing an internal error.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code == "23505" // unique_violation
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
