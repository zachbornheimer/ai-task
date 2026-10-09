package execution

import (
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

func TestTokens(t *testing.T) {
	tok := NewToken()
	if _, err := ParseToken(string(tok)); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseToken(" " + string(tok) + "\n"); err != nil {
		t.Fatal("whitespace around a token must be tolerated (stdin transport)")
	}
	if tok.Digest() == NewToken().Digest() || len(tok.Digest()) != 64 {
		t.Fatal("digest")
	}
	for _, bad := range []string{"", "sess-", "task-0123456789abcdef", string(tok)[:10]} {
		if _, err := ParseToken(bad); !fault.Is(err, fault.CodeInvalidSession) {
			t.Errorf("ParseToken(%q) = %v", bad, err)
		}
	}
}

func TestLeaseDuration(t *testing.T) {
	if d, _ := LeaseDuration(0); d != DefaultLease {
		t.Fatal("default")
	}
	if _, err := LeaseDuration(500 * time.Millisecond); !fault.Is(err, fault.CodeInvalidInput) {
		t.Fatal("too short accepted")
	}
	if _, err := LeaseDuration(48 * time.Hour); !fault.Is(err, fault.CodeInvalidInput) {
		t.Fatal("too long accepted")
	}
}

func TestAuthorize(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ended := now.Add(-time.Minute)
	live := Attempt{TaskID: "task-x", Seq: 2, LeaseExpiresAt: now.Add(time.Minute)}
	cases := []struct {
		name string
		a    Attempt
		cur  int
		code fault.Code
	}{
		{"ok", live, 2, ""},
		{"superseded", live, 3, fault.CodeSessionSuperseded},
		{"expired", Attempt{Seq: 2, LeaseExpiresAt: now}, 2, fault.CodeLeaseExpired},
		{"finished", Attempt{Seq: 2, LeaseExpiresAt: now.Add(time.Hour), EndedAt: &ended, EndReason: EndFinished}, 2, fault.CodeSessionFinished},
		{"ended other", Attempt{Seq: 2, LeaseExpiresAt: now.Add(time.Hour), EndedAt: &ended, EndReason: EndSuperseded}, 2, fault.CodeSessionSuperseded},
	}
	for _, c := range cases {
		if got := fault.CodeOf(Authorize(c.a, c.cur, now)); got != c.code {
			t.Errorf("%s: %q", c.name, got)
		}
	}
	if !live.LeaseActive(now) || live.Interrupted(now) {
		t.Fatal("live attempt state")
	}
	exp := Attempt{LeaseExpiresAt: now}
	if exp.LeaseActive(now) || !exp.Interrupted(now) {
		t.Fatal("expired attempt state")
	}
}

func TestLogEntryValidate(t *testing.T) {
	e := LogEntry{Done: "  x "}
	if err := e.Validate(); err != nil || e.Done != "x" {
		t.Fatal(err)
	}
	empty := LogEntry{Note: "   "}
	if err := empty.Validate(); !fault.Is(err, fault.CodeInvalidInput) {
		t.Fatal("empty entry accepted")
	}
}

func TestHandoffWarnings(t *testing.T) {
	h := Handoff{TotalLogs: 3, RecentLogs: make([]RecordedLog, 3)}
	if w := h.ComputeWarnings(); len(w) != 0 {
		t.Fatal(w)
	}
	h = Handoff{TotalLogs: LogWarnThreshold + 1, RecentLogs: make([]RecordedLog, RecentLogLimit)}
	if w := h.ComputeWarnings(); len(w) != 2 {
		t.Fatal(w)
	}
}
