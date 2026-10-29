package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
)

func init() {
	register("sync", "copy local state into a remote database: atask sync [--from PATH] [--to URL] [--dry-run]", runSync)
}

// runSync copies every business table of a local SQLite database into a
// destination database (a postgres URL for shared state, or another file),
// upserting by primary key so a rerun is a no-op. The local database is
// never written and stays the default; sync is the only path that touches
// the remote one.
func runSync(ctx context.Context, c *ctxt, args []string) error {
	var from, to string
	var dryRun bool
	c.fs.StringVar(&from, "from", "", "source SQLite path (default: the local state path, ATASK_DB)")
	c.fs.StringVar(&to, "to", "", "destination database URL or path (default: ATASK_DB_URL)")
	c.fs.BoolVar(&dryRun, "dry-run", false, "print per-table counts and write nothing")
	if err := c.parse(args); err != nil {
		return err
	}
	if from == "" {
		p, err := app.DefaultLocalPath()
		if err != nil {
			return err
		}
		from = p
	}
	if sqlite.IsPostgresURL(from) {
		return usage("sync reads a local SQLite path; %s is remote: pass --from PATH", sqlite.Redacted(from))
	}
	if to == "" {
		if to = c.env.Getenv("ATASK_DB_URL"); to == "" {
			return usage("sync needs a destination: --to URL (postgres://...) or ATASK_DB_URL")
		}
	}
	src, err := sqlite.Open(ctx, from)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = src.Close() }()
	dst, err := sqlite.Open(ctx, to)
	if err != nil {
		return fmt.Errorf("open destination: %w", err)
	}
	defer func() { _ = dst.Close() }()
	counts, err := sqlite.Sync(ctx, src, dst, dryRun)
	if err != nil {
		return err
	}
	rows := make([]map[string]any, 0, len(counts))
	total := 0
	for _, ct := range counts {
		total += ct.Rows
		rows = append(rows, map[string]any{"table": ct.Table, "rows": ct.Rows})
	}
	res := struct {
		From   string           `json:"from"`
		To     string           `json:"to"`
		DryRun bool             `json:"dry_run"`
		Tables []map[string]any `json:"tables"`
		Rows   int              `json:"rows"`
	}{From: sqlite.Redacted(from), To: sqlite.Redacted(to), DryRun: dryRun, Tables: rows, Rows: total}
	return c.emit(res, func(w io.Writer) {
		verb := "synced"
		if dryRun {
			verb = "would sync"
		}
		sorted := make([]sqlite.SyncCount, len(counts))
		copy(sorted, counts)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].Table < sorted[j].Table })
		for _, ct := range sorted {
			say(w, "%s %-22s %s\n", verb, ct.Table, strconv.Itoa(ct.Rows))
		}
		say(w, "● %s %d rows from %s to %s\n", verb, total, sqlite.Redacted(from), sqlite.Redacted(to))
	})
}
