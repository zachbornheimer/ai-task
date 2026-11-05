package app

import (
	"sort"

	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
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
// view is built against and its derived status.
type listed struct {
	rec    *sqlite.Record
	proj   project.Project
	status task.Status
}

// historyLimit is the bound a query applies, and Limit 0 means the default.
// Every filter that can return finished work is bounded; ready, blocked,
// draft and open list only unfinished work, which is never paged.
func historyLimit(f Filter, limit int) int {
	switch f {
	case FilterComplete, FilterArchived, FilterAll:
	default:
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

// isHistory reports whether a row counts toward the bound under f: every
// row of the complete and archived filters, and only the completed rows of
// the default listing, so open work is never hidden by it.
func isHistory(r listed, f Filter) bool {
	return f != FilterAll || r.status == task.StatusComplete
}

// newestHistory keeps the limit most recently completed (or archived) of
// the history rows, plus every other row, in their original order, and
// reports how many history rows it left out. A limit of NoLimit, or one the
// history does not exceed, keeps everything.
func newestHistory(rows []listed, f Filter, limit int) (kept []listed, omitted int) {
	if limit == NoLimit {
		return rows, 0
	}
	var history []int
	for i := range rows {
		if isHistory(rows[i], f) {
			history = append(history, i)
		}
	}
	if len(history) <= limit {
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
	sort.SliceStable(history, func(a, b int) bool { return stamp(rows[history[a]]) > stamp(rows[history[b]]) })
	dropped := make(map[int]bool, len(history)-limit)
	for _, i := range history[limit:] {
		dropped[i] = true
	}
	kept = make([]listed, 0, len(rows)-len(dropped))
	for i := range rows {
		if !dropped[i] {
			kept = append(kept, rows[i])
		}
	}
	return kept, len(dropped)
}
