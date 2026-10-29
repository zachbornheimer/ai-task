package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/must"
)

// TestSyncCopiesEveryBusinessTable seeds a source store with one row in
// every wide table, syncs it into a fresh destination, and proves the copy
// by reading the same facts back through the public query methods.
func TestSyncCopiesEveryBusinessTable(t *testing.T) {
	ctx := context.Background()
	src, err := Open(ctx, filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must.OK(t, src.Close()) }()
	seedSyncSource(t, ctx, src)

	dst, err := Open(ctx, filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must.OK(t, dst.Close()) }()
	counts, err := Sync(ctx, src, dst, false)
	if err != nil {
		t.Fatal(err)
	}
	byTable := map[string]int{}
	for _, ct := range counts {
		byTable[ct.Table] = ct.Rows
	}
	want := map[string]int{
		"projects": 1, "tasks": 1, "acceptance_criteria": 1,
		"execution_attempts": 2, // one ended, one live
		"task_log_entries":   1, "submissions": 1, "verification_runs": 1,
		"verification_results": 1, "run_results": 1, "check_history": 1,
		"task_projects": 1, "actions": 1, "verification_jobs": 1,
	}
	for table, rows := range want {
		if byTable[table] != rows {
			t.Fatalf("table %s synced %d rows; want %d", table, byTable[table], rows)
		}
	}
	for _, table := range []string{"plan_changes", "integration_records", "integration_intents", "task_dependencies"} {
		if byTable[table] != 0 {
			t.Fatalf("empty table %s synced %d rows; want 0", table, byTable[table])
		}
	}

	// The copied facts read back identically: the task with its
	// denormalized facts, the live attempt by digest, the log entry, and
	// the recorded check change.
	var (
		gotTask  Record
		attempt  AttemptAuth
		logs     int
		changes  int
		directID int64
	)
	must.OK(t, dst.Read(ctx, func(tx *Tx) error {
		var err error
		if gotTask, err = tx.GetRecord("at-src1"); err != nil {
			return err
		}
		if attempt, err = tx.AttemptByDigest("digest-src-2"); err != nil {
			return err
		}
		if logs, err = tx.LogCount("at-src1"); err != nil {
			return err
		}
		cs, err := tx.CheckChanges("at-src1", time.Time{})
		if err != nil {
			return err
		}
		changes = len(cs)
		return wrapInternal(tx.tx.QueryRowContext(ctx, `SELECT id FROM execution_attempts WHERE token_digest = ?`, "digest-src-2").Scan(&directID), "direct id")
	}))
	if gotTask.Task.Description != "source task one" {
		t.Fatalf("description did not survive sync: %q", gotTask.Task.Description)
	}
	if gotTask.UnmetRequires != 0 || gotTask.Failures != 1 {
		t.Fatalf("denormalized facts did not survive sync: %+v", gotTask)
	}
	if attempt.Attempt.Seq != 2 || attempt.CurrentSeq != 2 {
		t.Fatalf("attempt or generation did not survive sync: %+v", attempt)
	}
	if attempt.Attempt.ID != directID {
		t.Fatalf("the engine-allocated id must be copied verbatim: %d vs %d", attempt.Attempt.ID, directID)
	}
	if logs != 1 || changes != 1 {
		t.Fatalf("log (%d) or check change (%d) counts differ after sync", logs, changes)
	}
}

// TestSyncIsIdempotent: a rerun upserts the same rows and the destination
// holds exactly the source's counts.
func TestSyncIsIdempotent(t *testing.T) {
	ctx := context.Background()
	src, err := Open(ctx, filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must.OK(t, src.Close()) }()
	seedSyncSource(t, ctx, src)
	dst, err := Open(ctx, filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must.OK(t, dst.Close()) }()
	if _, err := Sync(ctx, src, dst, false); err != nil {
		t.Fatal(err)
	}
	counts, err := Sync(ctx, src, dst, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, ct := range counts {
		var n int
		must.OK(t, dst.Read(ctx, func(tx *Tx) error {
			return wrapInternal(tx.tx.QueryRowContext(ctx, `SELECT count(*) FROM `+ct.Table).Scan(&n), "count "+ct.Table)
		}))
		if n != ct.Rows {
			t.Fatalf("after a rerun %s holds %d rows but sync read %d", ct.Table, n, ct.Rows)
		}
	}
}

// TestSyncDryRunWritesNothing: dry-run counts the source and leaves the
// destination empty.
func TestSyncDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	src, err := Open(ctx, filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must.OK(t, src.Close()) }()
	seedSyncSource(t, ctx, src)
	dst, err := Open(ctx, filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must.OK(t, dst.Close()) }()
	counts, err := Sync(ctx, src, dst, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != len(syncTables) {
		t.Fatalf("dry run counted %d tables; want %d", len(counts), len(syncTables))
	}
	for _, ct := range counts {
		if ct.Rows == 0 {
			continue
		}
		var n int
		must.OK(t, dst.Read(ctx, func(tx *Tx) error {
			return wrapInternal(tx.tx.QueryRowContext(ctx, `SELECT count(*) FROM `+ct.Table).Scan(&n), "count "+ct.Table)
		}))
		if n != 0 {
			t.Fatalf("dry run wrote %d rows into %s", n, ct.Table)
		}
	}
}

// seedSyncSource fills a source store with one row in every wide table
// (raw SQL, as migrate_test seeds), so the copy exercises every shape:
// a project, a task with acceptance and projects membership, a check
// change, two attempts (one ended, one live), a log entry, a submission,
// a passed run, its evidence, the run result, an action, and a job.
func seedSyncSource(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()
	at := int64(1_700_000_000_000)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO projects (id, name, root_path, created_at, updated_at) VALUES ('proj-src', 'source', NULL, ?, ?)`, at, at)
	exec(`INSERT INTO tasks (id, project_id, kind, description, outcome, policy_json, policy_digest, failures, created_at, updated_at)
		VALUES ('at-src1', 'proj-src', 'task', 'source task one', 'copied', ?, ?, 1, ?, ?)`,
		`{"task_checks":[{"id":"proof","required":true}]}`, "digest-policy", at, at)
	exec(`INSERT INTO acceptance_criteria (task_id, position, description) VALUES ('at-src1', 0, 'copied too')`)
	exec(`INSERT INTO task_projects (task_id, project_id, position) VALUES ('at-src1', 'proj-src', 0)`)
	exec(`INSERT INTO execution_attempts (id, task_id, seq, token_digest, started_at, lease_expires_at, ended_at, end_reason, lease_ms) VALUES (11, 'at-src1', 1, 'digest-src-1', ?, ?, ?, 'superseded', 60000)`, at, at+1000, at+2000)
	exec(`INSERT INTO execution_attempts (id, task_id, seq, token_digest, started_at, lease_expires_at, lease_ms) VALUES (12, 'at-src1', 2, 'digest-src-2', ?, ?, 60000)`, at+2000, at+3_600_000)
	exec(`INSERT INTO task_log_entries (id, task_id, attempt_id, done, next, created_at) VALUES (21, 'at-src1', 12, 'done one', 'next one', ?)`, at+3000)
	exec(`INSERT INTO submissions (id, task_id, attempt_id, revision, submitted_at) VALUES (31, 'at-src1', 12, 'deadbeef', ?)`, at+4000)
	exec(`UPDATE tasks SET current_attempt_seq = 2, latest_submission_id = 31, completed_at = ?, completed_submission_id = 31 WHERE id = 'at-src1'`, at+6000)
	exec(`INSERT INTO verification_runs (id, task_id, attempt_id, submission_id, mode, status, revision, policy_json, policy_digest, created_at, started_at, finished_at, summary)
		VALUES (41, 'at-src1', 12, 31, 'complete', 'passed', 'deadbeef', '{}', 'digest-policy', ?, ?, ?, 'clean')`, at+5000, at+5000, at+5001)
	exec(`INSERT INTO verification_results (id, run_id, check_id, check_digest, required, revision, policy_digest, started_at, finished_at, exit_code, outcome, reused)
		VALUES (51, 41, 'proof', 'digest-check', 1, 'deadbeef', 'digest-policy', ?, ?, 0, 'passed', 0)`, at+5000, at+5000)
	exec(`INSERT INTO run_results (id, task_id, attempt_id, run_id, evidence_id, check_type, preset, subject, options_json, started_at, elapsed_ms, budget_ms, timed_out, passed, output)
		VALUES (61, 'at-src1', 12, 41, 51, 'proof', 'run', 'go test', '{}', ?, 10, 30000, 0, 1, 'ok')`, at+5000)
	exec(`INSERT INTO actions (task_id, position, preset, subject, options_json, detach, status) VALUES ('at-src1', 0, 'run', 'echo done', '{}', 0, 'pending')`)
	exec(`INSERT INTO check_history (id, task_id, changed_at, field, old_spec, new_spec, actor, cwd) VALUES (71, 'at-src1', ?, 'proof', 'run: old', 'run: new', 'planner', '/tmp')`, at+100)
	exec(`INSERT INTO verification_jobs (id, project_id, cohort, status, members_json, owner_digest, lease_expires_at, created_at) VALUES (81, 'proj-src', 'g', 'passed', '[]', 'owner', ?, ?)`, at, at)
}
