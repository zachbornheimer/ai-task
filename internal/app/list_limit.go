package app

import (
	"sort"

	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
)

const (
	// DefaultHistoryLimit is how many completed or archived tasks a listing
	// returns when the query sets no Limit: the history only grows, and an
	// unbounded page of it is minutes on a remote board.
	DefaultHistoryLimit = 200
	// NoLimit as ListQuery.Limit lifts the bound.
	NoLimit = -1
)

// listed is a record that passed a listing's filter, with the project its
// view is built against.
type listed struct {
	rec  *sqlite.Record
	proj project.Project
}

// historyLimit is the bound a query applies: only the history filters are
// bounded (open work is never paged), and Limit 0 means the default.
func historyLimit(f Filter, limit int) int {
	if f != FilterComplete && f != FilterArchived {
		return NoLimit
	}
	switch {
	case limit == 0:
		return DefaultHistoryLimit
	case limit < 0:
		return NoLimit
	}
	return limit
}

// newestHistory keeps the limit most recently completed (or archived) of
// rows, in their original order, and reports how many it left out. A
// limit of NoLimit, or one the rows do not exceed, keeps everything.
func newestHistory(rows []listed, f Filter, limit int) (kept []listed, omitted int) {
	if limit == NoLimit || len(rows) <= limit {
		return rows, 0
	}
	stamp := func(r listed) int64 {
		if f == FilterArchived && r.rec.Task.ArchivedAt != nil {
			return r.rec.Task.ArchivedAt.UnixMilli()
		}
		if r.rec.CompletedAt != nil {
			return r.rec.CompletedAt.UnixMilli()
		}
		return 0
	}
	newest := make([]int, len(rows))
	for i := range newest {
		newest[i] = i
	}
	sort.SliceStable(newest, func(a, b int) bool { return stamp(rows[newest[a]]) > stamp(rows[newest[b]]) })
	newest = newest[:limit]
	sort.Ints(newest)
	kept = make([]listed, 0, limit)
	for _, i := range newest {
		kept = append(kept, rows[i])
	}
	return kept, len(rows) - limit
}
