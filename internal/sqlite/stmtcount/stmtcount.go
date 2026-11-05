// Package stmtcount wraps the SQLite driver so a test can count and
// classify every statement and transaction a store issues. Round trips are
// the cost that matters on a remote board, and they are invisible on a
// local file; a test that counts them keeps that cost from regressing
// without needing a network.
package stmtcount

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// Counter records what a counting driver saw. It is safe for concurrent use.
type Counter struct {
	mu         sync.Mutex
	statements []string
	begins     int
	writeBegin int
}

// Reset forgets everything counted so far.
func (c *Counter) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statements, c.begins, c.writeBegin = nil, 0, 0
}

// Statements is how many statements ran (queries and execs).
func (c *Counter) Statements() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.statements)
}

// Transactions is how many transactions began.
func (c *Counter) Transactions() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.begins
}

// WriteTransactions is how many transactions began that were not read-only.
func (c *Counter) WriteTransactions() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeBegin
}

// Writes returns the statements that change data or schema: anything that
// does not start with SELECT, WITH, PRAGMA or EXPLAIN.
func (c *Counter) Writes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, s := range c.statements {
		if !isRead(s) {
			out = append(out, s)
		}
	}
	return out
}

func isRead(stmt string) bool {
	word := strings.ToUpper(strings.Fields(strings.TrimSpace(stmt) + " ")[0])
	switch word {
	case "SELECT", "WITH", "PRAGMA", "EXPLAIN":
		return true
	}
	return false
}

func (c *Counter) statement(q string) {
	c.mu.Lock()
	c.statements = append(c.statements, q)
	c.mu.Unlock()
}

func (c *Counter) begin(readOnly bool) {
	c.mu.Lock()
	c.begins++
	if !readOnly {
		c.writeBegin++
	}
	c.mu.Unlock()
}

var registrations atomic.Int64

// Register installs a driver that wraps the SQLite driver and reports to the
// returned Counter, and returns the name to open a store with. The name
// carries a process-wide sequence number, so registering again (a test run
// with -count=2) never collides: database/sql panics on a repeated name.
func Register(prefix string) (name string, counter *Counter) {
	name = fmt.Sprintf("%s-%d", prefix, registrations.Add(1))
	counter = &Counter{}
	sql.Register(name, &countingDriver{counter: counter})
	return name, counter
}

type countingDriver struct {
	counter *Counter
}

func (d *countingDriver) Open(dsn string) (driver.Conn, error) {
	db, err := sql.Open("sqlite", "")
	if err != nil {
		return nil, err
	}
	inner := db.Driver()
	_ = db.Close()
	conn, err := inner.Open(dsn)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: conn, counter: d.counter}, nil
}

type countingConn struct {
	driver.Conn
	counter *Counter
}

func (c *countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.counter.begin(opts.ReadOnly)
	b, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.New("stmtcount: the wrapped driver does not implement driver.ConnBeginTx")
	}
	return b.BeginTx(ctx, opts)
}

func (c *countingConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.counter.statement(q)
	e, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return e.ExecContext(ctx, q, args)
}

func (c *countingConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.counter.statement(q)
	qr, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return qr.QueryContext(ctx, q, args)
}

func (c *countingConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	if p, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, q)
	}
	return c.Conn.Prepare(q)
}
