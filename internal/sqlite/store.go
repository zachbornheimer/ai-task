// Package sqlite is the authoritative store. It knows the schema and SQL and
// nothing about what the facts mean: eligibility, authority, and completion
// are decided by the domain packages using the facts this package returns.
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

	_ "modernc.org/sqlite" // pure-Go SQLite driver; see docs/decisions/0002-sqlite.md

	"github.com/zachbornheimer/ai-task/internal/fault"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// BusyTimeout is how long a connection waits for a lock before failing. CLI
// invocations are short, so contention resolves in milliseconds in practice;
// the generous bound covers a slow verification-result commit.
const BusyTimeout = 10 * time.Second

// Store wraps one SQLite database file.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (creating if needed) the database at path and applies pending
// migrations. ":memory:" opens a private in-memory database for tests; it is
// limited to a single connection so every statement sees the same database.
func Open(ctx context.Context, path string) (*Store, error) {
	memory := path == ":memory:"
	if !memory {
		path = filepath.Clean(path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fault.Wrap(err, fault.CodeInternal, "create state directory")
		}
	}
	dsn := dsnFor(path, memory)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fault.Wrap(err, fault.CodeInternal, "open database")
	}
	if memory {
		db.SetMaxOpenConns(1)
	}
	s := &Store{db: db, path: path}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
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
	// serialises them. Read-only transactions still begin deferred.
	q.Set("_txlock", "immediate")
	return "file:" + path + "?" + q.Encode()
}

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// Close releases the connection pool.
func (s *Store) Close() error { return s.db.Close() }

// Tx is a transaction handle exposing typed queries. All methods must be
// called before the enclosing Read/Write callback returns.
type Tx struct {
	tx  *sql.Tx
	ctx context.Context
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
		return fault.Wrap(err, fault.CodeInternal, "begin transaction")
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(&Tx{tx: tx, ctx: ctx}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fault.Wrap(err, fault.CodeInternal, "commit transaction")
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return fault.Wrap(err, fault.CodeInternal, "read migrations")
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
		return fault.Wrap(err, fault.CodeInternal, "create schema_migrations")
	}
	return s.Write(ctx, func(tx *Tx) error {
		applied := map[int64]bool{}
		rows, err := tx.tx.QueryContext(ctx, `SELECT version FROM schema_migrations`)
		if err != nil {
			return fault.Wrap(err, fault.CodeInternal, "read schema_migrations")
		}
		for rows.Next() {
			var v int64
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return err
			}
			applied[v] = true
		}
		rows.Close()
		for _, name := range names {
			version, err := strconv.ParseInt(strings.SplitN(name, "_", 2)[0], 10, 64)
			if err != nil {
				return fmt.Errorf("migration %q: bad version prefix", name)
			}
			if applied[version] {
				continue
			}
			body, err := migrationFS.ReadFile("migrations/" + name)
			if err != nil {
				return err
			}
			if _, err := tx.tx.ExecContext(ctx, string(body)); err != nil {
				return fault.Wrap(err, fault.CodeInternal, "apply migration %s", name)
			}
			if _, err := tx.tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`, version, name, time.Now().UnixMilli()); err != nil {
				return fault.Wrap(err, fault.CodeInternal, "record migration %s", name)
			}
		}
		return nil
	})
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
	nullStr = sql.NullString
)

func wrapInternal(err error, what string) error {
	if err == nil {
		return nil
	}
	return fault.Wrap(err, fault.CodeInternal, "%s", what)
}

// IsUniqueViolation reports whether err is a UNIQUE/PRIMARY KEY conflict.
// Callers use it to retry random-ID generation on the (astronomically rare)
// collision instead of surfacing an internal error.
func IsUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
