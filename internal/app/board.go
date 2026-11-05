package app

import (
	"time"

	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// board is the read model a listing assembles in one transaction: the
// records and projects the listing itself already loaded, plus those
// records' check history and requirements loaded set-based. A view built
// on a board resolves its per-row relations from memory and falls back to
// a query for any row the listing did not load.
//
// The board is what keeps a listing flat. Without it a listing pays one
// round trip per relation of every row, which is invisible on a local
// SQLite file and, on a remote store, minutes for a board of any size:
// each round trip costs tens of milliseconds over the network, so a
// thousand-row board pays thousands of them. See the remote-listing
// regression test for the bound this must hold.
type board struct {
	tx *sqlite.Tx
	// records are the listing's own rows; a related task in them resolves
	// without a query.
	records map[task.ID]sqlite.Record
	// projects is the listing's project cache; nil means none was loaded.
	projects map[project.ID]project.Project
	// checkChanges and reqs are the set-based loads of the listed rows'
	// check history and requirements.
	checkChanges map[task.ID][]task.CheckChange
	reqs         map[task.ID][]task.ID
}

// emptyBoard resolves every relation with a query: the single-task paths,
// which list nothing and so preload nothing.
func emptyBoard(tx *sqlite.Tx) *board {
	return &board{tx: tx}
}

// newBoard loads the relations of one listing's records: their check
// history and their requirements, set-based, in a bounded number of
// queries. records are the listing's own rows; projects is its project
// cache, nil when it loaded none.
func newBoard(tx *sqlite.Tx, records []sqlite.Record, projects map[project.ID]project.Project) (*board, error) {
	b := &board{tx: tx, projects: projects}
	b.records = make(map[task.ID]sqlite.Record, len(records))
	ids := make([]task.ID, len(records))
	for i := range records {
		r := &records[i]
		b.records[r.Task.ID] = *r
		ids[i] = r.Task.ID
	}
	changes, err := tx.CheckChangesFor(ids)
	if err != nil {
		return nil, err
	}
	b.checkChanges = changes
	reqs, err := tx.RequirementIDsFor(ids)
	if err != nil {
		return nil, err
	}
	b.reqs = reqs
	// A listing views every group's members and its own runs: preload both
	// for the listed groups, the only rows a listing asks for either.
	var groups []task.ID
	for i := range records {
		r := &records[i]
		if r.Task.Kind == task.KindGroup {
			groups = append(groups, r.Task.ID)
		}
	}
	if err := tx.PreloadChildren(groups); err != nil {
		return nil, err
	}
	if err := tx.PreloadRuns(groups); err != nil {
		return nil, err
	}
	return b, nil
}

// record resolves one related task: the listing's own row when it loaded
// it, a query when it did not (an archived parent, for example, is not
// part of a live listing).
func (b *board) record(id task.ID) (sqlite.Record, error) {
	if r, ok := b.records[id]; ok {
		return r, nil
	}
	return b.tx.GetRecord(id)
}

// project resolves one project of a listed task: the listing's cache when
// it holds it, a query when it does not. A queried project is remembered,
// so a listing that names several foreign tasks pays each home once.
func (b *board) project(id project.ID) (project.Project, error) {
	if p, ok := b.projects[id]; ok {
		return p, nil
	}
	p, err := b.tx.GetProject(id)
	if err == nil {
		if b.projects == nil {
			b.projects = map[project.ID]project.Project{}
		}
		b.projects[id] = p
	}
	return p, err
}

// changes resolves one task's recorded check changes: the set-based load
// when the listing loaded the task, the per-task query when it did not.
// since filters in memory exactly as the query filters in SQL: a stored
// change lands at or after since's millisecond floor.
func (b *board) changes(id task.ID, since time.Time) ([]task.CheckChange, error) {
	if cs, ok := b.checkChanges[id]; ok {
		out := make([]task.CheckChange, 0, len(cs))
		for _, c := range cs {
			if c.ChangedAt.UnixMilli() >= since.UnixMilli() {
				out = append(out, c)
			}
		}
		return out, nil
	}
	return b.tx.CheckChanges(id, since)
}

// requirementIDs resolves one task's requirement IDs: the set-based load
// when the listing loaded the task, the per-task query when it did not.
func (b *board) requirementIDs(id task.ID) ([]task.ID, error) {
	if ids, ok := b.reqs[id]; ok {
		return ids, nil
	}
	return b.tx.RequirementIDs(id)
}
