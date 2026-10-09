package execution

import (
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// LogEntry is what an agent records. Each field has one meaning:
//
//	Done    - progress made (a claim, not verified)
//	Next    - remaining work or the recommended next action
//	Learned - a reusable discovery worth handing to the next attempt
//	Note    - a warning, question, or other context
type LogEntry struct {
	Done    string `json:"done,omitempty"`
	Next    string `json:"next,omitempty"`
	Learned string `json:"learned,omitempty"`
	Note    string `json:"note,omitempty"`
}

const maxLogField = 20000

// Validate trims fields and requires at least one to be set.
func (e *LogEntry) Validate() error {
	e.Done = strings.TrimSpace(e.Done)
	e.Next = strings.TrimSpace(e.Next)
	e.Learned = strings.TrimSpace(e.Learned)
	e.Note = strings.TrimSpace(e.Note)
	if e.Done == "" && e.Next == "" && e.Learned == "" && e.Note == "" {
		return fault.New(fault.CodeInvalidInput, "log entry needs at least one of --done, --next, --learned, --note")
	}
	for _, f := range []string{e.Done, e.Next, e.Learned, e.Note} {
		if len(f) > maxLogField {
			return fault.New(fault.CodeInvalidInput, "log field exceeds %d characters", maxLogField)
		}
	}
	return nil
}

// RecordedLog is a committed entry. Seq is a global, strictly increasing
// sequence so ordering is deterministic even for entries in the same
// millisecond.
type RecordedLog struct {
	Seq        int64     `json:"seq"`
	AttemptSeq int       `json:"attempt"`
	At         time.Time `json:"at"`
	LogEntry
}

// Handoff bounds. Take and Show return a bounded slice of history so that an
// agent's context is not flooded; the full history is available separately.
const (
	RecentLogLimit   = 10
	LearningLimit    = 50
	LogWarnThreshold = 200
)

// Handoff is the context a new attempt starts from.
type Handoff struct {
	// LatestNext is the Next field of the most recent entry that set one.
	LatestNext string `json:"latest_next,omitempty"`
	// Learnings are the Learned fields of past entries, newest first,
	// bounded by LearningLimit.
	Learnings []string `json:"learnings,omitempty"`
	// RecentLogs are the newest entries, newest first, bounded by
	// RecentLogLimit.
	RecentLogs []RecordedLog `json:"recent_logs,omitempty"`
	// TotalLogs is the full count, so an agent knows when history was cut.
	TotalLogs int `json:"total_logs"`
	// Warnings are advisory (e.g. the log is large enough to suggest the
	// task scope or handoff quality needs review).
	Warnings []string `json:"warnings,omitempty"`
}

// Warnings derives advisory messages from a handoff's size.
func (h Handoff) ComputeWarnings() []string {
	var w []string
	if h.TotalLogs > LogWarnThreshold {
		w = append(w, "this task has an unusually long log; consider whether its scope is one semantic outcome or whether handoffs are repeating work")
	}
	if len(h.RecentLogs) < h.TotalLogs {
		w = append(w, "history truncated; run `tasks history <task>` for the full log")
	}
	return w
}
