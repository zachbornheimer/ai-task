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
	// Inherited carries learnings recorded on this task's direct
	// prerequisites: their outputs are this task's inputs.
	Inherited []InheritedLearning `json:"inherited,omitempty"`
	// LastFailure summarises the newest failed verification run so a new
	// attempt does not start blind.
	LastFailure *Failure `json:"last_failure,omitempty"`
}

// Empty reports whether the handoff carries nothing worth showing.
func (h Handoff) Empty() bool {
	return h.TotalLogs == 0 && len(h.Inherited) == 0 && h.LastFailure == nil && len(h.Warnings) == 0
}

// InheritedLearning is a learning from a prerequisite task.
type InheritedLearning struct {
	TaskID  string `json:"task_id"`
	Key     string `json:"key,omitempty"`
	Learned string `json:"learned"`
}

// Failure is the bounded summary of a failed verification run.
type Failure struct {
	RunID   int64         `json:"run_id"`
	Mode    string        `json:"mode"`
	Summary string        `json:"summary"`
	Checks  []FailedCheck `json:"checks,omitempty"`
}

// FailedCheck is one non-passing check with a bounded output tail.
type FailedCheck struct {
	CheckID    string `json:"check_id"`
	Outcome    string `json:"outcome"`
	Message    string `json:"message,omitempty"`
	StderrTail string `json:"stderr_tail,omitempty"`
	StdoutTail string `json:"stdout_tail,omitempty"`
}

// Bounds for inherited context so a claim payload stays small.
const (
	InheritedLearningLimit = 20
	FailureTailBytes       = 1500
)

// Tail returns the last n bytes of s, cut at a line boundary when possible.
func Tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[len(s)-n:]
	if i := strings.IndexByte(cut, '\n'); i >= 0 && i < len(cut)-1 {
		cut = cut[i+1:]
	}
	return "…" + cut
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
