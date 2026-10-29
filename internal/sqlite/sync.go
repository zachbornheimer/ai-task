package sqlite

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/discard"
)

// Sync copies every business table of src into dst, upserting by primary
// key, and reports how many rows of each table it read. It is idempotent:
// a destination row whose key already exists is overwritten with the
// source's values. schema_migrations is never copied (each store owns its
// own ledger) and neither are SQLite's internal tables.
//
// The destination may be any store this package opens: a PostgreSQL URL
// (the sync use case) or another SQLite file (tests). Both schemas are the
// final migration state this package owns, so the columns and their order
// match by construction.
func Sync(ctx context.Context, src, dst *Store, dryRun bool) ([]SyncCount, error) {
	counts := make([]SyncCount, 0, len(syncTables))
	for _, t := range syncTables {
		cols, rows, err := readTable(ctx, src, t.name)
		if err != nil {
			return counts, err
		}
		counts = append(counts, SyncCount{Table: t.name, Rows: len(rows)})
		if dryRun {
			continue
		}
		if err := writeTable(ctx, dst, t, cols, rows); err != nil {
			return counts, err
		}
	}
	return counts, nil
}

// SyncCount is how many rows of one table sync read from the source.
type SyncCount struct {
	Table string
	Rows  int
}

// syncTable is one business table, in foreign-key order, with its primary
// key columns (the conflict target of the upsert) and, where the engine
// allocates row ids, the id column.
type syncTable struct {
	name string
	pk   []string
	// identity is the table's engine-allocated id column ("" when the
	// primary key is caller-assigned): the column whose sequence a sync
	// must restart past the ids it copied.
	identity string
}

// syncTables lists every table after the final migration, parents before
// children, so one pass satisfies every foreign key. schema_migrations is
// absent on purpose: each store records its own schema state.
var syncTables = []syncTable{
	{"projects", []string{"id"}, ""},
	{"tasks", []string{"id"}, ""},
	{"acceptance_criteria", []string{"task_id", "position"}, ""},
	{"task_dependencies", []string{"task_id", "requires_id"}, ""},
	{"task_projects", []string{"task_id", "project_id"}, ""},
	{"execution_attempts", []string{"id"}, "id"},
	{"task_log_entries", []string{"id"}, "id"},
	{"submissions", []string{"id"}, "id"},
	{"verification_runs", []string{"id"}, "id"},
	{"verification_results", []string{"id"}, "id"},
	{"run_results", []string{"id"}, "id"},
	{"actions", []string{"task_id", "position"}, ""},
	{"check_history", []string{"id"}, "id"},
	{"plan_changes", []string{"project_id", "idempotency_key"}, ""},
	{"verification_jobs", []string{"id"}, "id"},
	{"integration_records", []string{"id"}, "id"},
	{"integration_intents", []string{"id"}, "id"},
}

// readTable returns every row of one table with its column names.
func readTable(ctx context.Context, s *Store, table string) ([]string, [][]any, error) {
	var cols []string
	var rows [][]any
	err := s.Read(ctx, func(tx *Tx) error {
		rs, err := tx.tx.QueryContext(ctx, `SELECT * FROM `+table)
		if err != nil {
			return wrapInternal(err, "read "+table)
		}
		defer discard.CloseReadOnly(rs)
		cols, err = rs.Columns()
		if err != nil {
			return wrapInternal(err, "columns of "+table)
		}
		for rs.Next() {
			vals := make([]any, len(cols))
			scan := make([]any, len(cols))
			for i := range vals {
				scan[i] = &vals[i]
			}
			if err := rs.Scan(scan...); err != nil {
				return wrapInternal(err, "scan row of "+table)
			}
			rows = append(rows, vals)
		}
		return wrapInternal(rs.Err(), "read rows of "+table)
	})
	return cols, rows, err
}

// syncBatchRows bounds one multi-row INSERT statement. PostgreSQL accepts
// at most 65535 parameters; the widest table here has 26 columns, so 50
// rows stay far under the limit and keep the statement text small.
const syncBatchRows = 50

// writeTable upserts rows into one table of the destination, in batches,
// inside one transaction per table.
func writeTable(ctx context.Context, dst *Store, t syncTable, cols []string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	updatable := updatableColumns(cols, t.pk)
	err := dst.Write(ctx, func(tx *Tx) error {
		for start := 0; start < len(rows); start += syncBatchRows {
			end := min(start+syncBatchRows, len(rows))
			batch := rows[start:end]
			if err := upsertBatch(ctx, tx, t, cols, updatable, batch); err != nil {
				return err
			}
		}
		if err := restartIdentity(ctx, tx, t); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("sync %s: %w", t.name, err)
	}
	return nil
}

// restartIdentity moves the destination's id allocation past the ids sync
// copied in, so the first row the engine inserts after a sync cannot
// collide with a synced one. SQLite needs nothing: its rowid and
// AUTOINCREMENT allocation already starts past the copied maximum.
func restartIdentity(ctx context.Context, tx *Tx, t syncTable) error {
	if t.identity == "" || !tx.d.isPostgres() {
		return nil
	}
	var max int64
	if err := tx.tx.QueryRowContext(ctx, `SELECT COALESCE(max(`+t.identity+`), 0) FROM `+t.name).Scan(&max); err != nil {
		return wrapInternal(err, "read max id of "+t.name)
	}
	_, err := tx.tx.ExecContext(ctx, `ALTER TABLE `+t.name+` ALTER COLUMN `+t.identity+` RESTART WITH `+strconv.FormatInt(max+1, 10))
	return wrapInternal(err, "restart id of "+t.name)
}

// upsertBatch inserts one batch of rows, updating the destination row when
// the primary key already exists.
func upsertBatch(ctx context.Context, tx *Tx, t syncTable, cols, updatable []string, batch [][]any) error {
	ph := make([]string, 0, len(batch))
	var args []any
	for _, row := range batch {
		one := make([]string, 0, len(cols))
		for _, v := range row {
			one = append(one, "?")
			args = append(args, v)
		}
		ph = append(ph, "("+strings.Join(one, ", ")+")")
	}
	set := make([]string, 0, len(updatable))
	for _, c := range updatable {
		set = append(set, c+" = EXCLUDED."+c)
	}
	q := "INSERT INTO " + t.name + " (" + strings.Join(cols, ", ") + ") VALUES " +
		strings.Join(ph, ", ") +
		" ON CONFLICT (" + strings.Join(t.pk, ", ") + ") DO UPDATE SET " + strings.Join(set, ", ")
	_, err := tx.tx.ExecContext(ctx, q, args...)
	return wrapInternal(err, "upsert into "+t.name)
}

// updatableColumns lists every column that an upsert overwrites: all but
// the primary key columns, which the conflict resolves.
func updatableColumns(cols, pk []string) []string {
	var out []string
	for _, c := range cols {
		if !contains(pk, c) {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		// A pure primary-key table has nothing to update; any conflict
		// means the row is already there.
		return cols
	}
	return out
}

func contains(xs []string, want string) bool {
	return slices.Contains(xs, want)
}
