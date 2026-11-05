package sqlite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/must"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// reconcileLease is the attempt lease the scenario grants; the scenario
// then reads the board an hour past it.
const reconcileLease = 30 * time.Minute

// openIsolatedRemote opens a PostgreSQL store on a schema of its own and
// drops the schema afterwards. ReconcileRuns acts on every row of the
// database it runs in, so the scenario below must never share tables with
// anything else.
func openIsolatedRemote(t *testing.T) *Store {
	t.Helper()
	raw := remoteURL(t)
	schema := fmt.Sprintf("atask_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	admin, _, err := openPostgres(raw)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, dropErr := admin.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE")
		must.OK(t, dropErr)
		must.OK(t, admin.Close())
	})
	return openRemoteStore(t, withParam(t, raw, "search_path", schema))
}

// TestReconcileScenarioOnSQLite and TestReconcileScenarioOnPostgreSQL run
// one scenario against both engines: the probe, the closing of dead runs
// and expired attempts, the cancel, and the reset all use the dialect's
// placeholder rewriting, and only a real PostgreSQL proves that.
func TestReconcileScenarioOnSQLite(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "at.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must.OK(t, s.Close()) }()
	exerciseReconcile(t, s)
}

func TestReconcileScenarioOnPostgreSQL(t *testing.T) {
	exerciseReconcile(t, openIsolatedRemote(t))
}

func exerciseReconcile(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	start := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	later := start.Add(time.Hour + reconcileLease)
	const id = task.ID("at-recon1")

	exec := func(q string, args ...any) {
		t.Helper()
		must.OK(t, s.Write(ctx, func(tx *Tx) error {
			_, err := tx.tx.ExecContext(ctx, q, args...)
			return err
		}))
	}
	scalar := func(q string, args ...any) (v string) {
		t.Helper()
		must.OK(t, s.Read(ctx, func(tx *Tx) error {
			return tx.tx.QueryRowContext(ctx, q, args...).Scan(&v)
		}))
		return v
	}
	pending := func(at time.Time) (p bool) {
		t.Helper()
		must.OK(t, s.Read(ctx, func(tx *Tx) (err error) { p, err = tx.ReconcilePending(at); return err }))
		return p
	}
	reconcile := func(at time.Time) (n int64) {
		t.Helper()
		must.OK(t, s.Write(ctx, func(tx *Tx) (err error) { n, err = tx.ReconcileRuns(at); return err }))
		return n
	}

	at := start.UnixMilli()
	exec(`INSERT INTO projects (id, name, root_path, created_at, updated_at) VALUES ('proj-recon', 'recon', NULL, ?, ?)`, at, at)
	exec(`INSERT INTO tasks (id, project_id, kind, description, outcome, policy_json, policy_digest, failures, created_at, updated_at)
		VALUES (?, 'proj-recon', 'task', 'recon', 'recon', '{}', 'digest', 0, ?, ?)`, id, at, at)

	var attempt execution.Attempt
	var runID int64
	must.OK(t, s.Write(ctx, func(tx *Tx) (err error) {
		if attempt, err = tx.StartAttempt(id, nil, "digest-1", start, reconcileLease); err != nil {
			return err
		}
		runID, err = tx.InsertRun(NewRun{TaskID: id, AttemptID: attempt.ID, Mode: verification.ModeComplete, Status: RunRunning, HeartbeatEvery: time.Minute}, start)
		return err
	}))

	if pending(start) {
		t.Fatal("a live claim with a fresh run reads as having something to reconcile")
	}
	if !pending(later) {
		t.Fatal("an hour past the lease the probe sees nothing pending")
	}
	if n := reconcile(later); n < 2 {
		t.Fatalf("reconcile changed %d rows, want the run and the attempt at least", n)
	}
	if got := scalar(`SELECT status FROM verification_runs WHERE id = ?`, runID); got != string(RunError) {
		t.Fatalf("dead run status = %q, want error", got)
	}
	if got := scalar(`SELECT end_reason FROM execution_attempts WHERE id = ?`, attempt.ID); got != string(execution.EndExpired) {
		t.Fatalf("expired attempt end_reason = %q, want expired", got)
	}
	if pending(later) {
		t.Fatal("the probe still sees work after a reconcile closed everything")
	}

	// A new claim, a running run, and a cancel of exactly that attempt.
	var second execution.Attempt
	var secondRun int64
	must.OK(t, s.Write(ctx, func(tx *Tx) (err error) {
		prev := attempt
		prev.EndedAt = new(time.Time)
		if second, err = tx.StartAttempt(id, &prev, "digest-2", later, reconcileLease); err != nil {
			return err
		}
		secondRun, err = tx.InsertRun(NewRun{TaskID: id, AttemptID: second.ID, Mode: verification.ModeComplete, Status: RunRunning, HeartbeatEvery: time.Minute}, later)
		return err
	}))
	var closed, again int64
	must.OK(t, s.Write(ctx, func(tx *Tx) (err error) {
		if closed, err = tx.CancelRunningRuns(second.ID, AbandonedPrefix+" canceled", later); err != nil {
			return err
		}
		again, err = tx.CancelRunningRuns(second.ID, AbandonedPrefix+" canceled", later)
		return err
	}))
	if closed != 1 || again != 0 {
		t.Fatalf("cancel closed %d then %d runs, want 1 then 0", closed, again)
	}
	if got := scalar(`SELECT summary FROM verification_runs WHERE id = ?`, secondRun); got != AbandonedPrefix+" canceled" {
		t.Fatalf("canceled run summary = %q", got)
	}

	// The second attempt lapses too; a reset turns the expired end into a
	// released one and reports the change.
	evenLater := later.Add(2 * reconcileLease)
	reconcile(evenLater)
	var changed bool
	must.OK(t, s.Write(ctx, func(tx *Tx) (err error) { changed, err = tx.ResetAttempts(id, evenLater); return err }))
	if !changed {
		t.Fatal("reset reported no change for an expired attempt")
	}
	if got := scalar(`SELECT end_reason FROM execution_attempts WHERE id = ?`, second.ID); got != string(execution.EndReleased) {
		t.Fatalf("after reset the attempt end_reason = %q, want released", got)
	}
	if got := scalar(`SELECT end_reason FROM execution_attempts WHERE id = ?`, attempt.ID); got != string(execution.EndExpired) {
		t.Fatalf("reset rewrote an older attempt: end_reason = %q, want expired", got)
	}

	// Opening a migrated store again changes nothing in the ledger.
	rows := scalar(`SELECT count(*) FROM schema_migrations`)
	must.OK(t, s.migrate(ctx))
	if got := scalar(`SELECT count(*) FROM schema_migrations`); got != rows {
		t.Fatalf("re-migrating a current store changed the ledger: %s rows, then %s", rows, got)
	}
}
