package task

import (
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func TestIDs(t *testing.T) {
	id := NewID()
	if len(id) != len("at-")+6 {
		t.Fatalf("bad length %q", id)
	}
	if _, err := ParseID(string(id)); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseID("task-0123456789abcdef"); err != nil {
		t.Fatal("legacy ids must still parse")
	}
	if _, err := ParseID("proj-0123456789abcdef"); !fault.Is(err, fault.CodeInvalidInput) {
		t.Fatal("wrong prefix accepted")
	}
	if !IsID("at-abc123") || IsID("oauth-endpoint") {
		t.Fatal("IsID")
	}
	for _, k := range []string{"oauth-endpoint", "a/b.c_d", "K1"} {
		if err := ValidateKey(k); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range []string{"-x", "has space", "at-abc123", strings.Repeat("k", 200)} {
		if err := ValidateKey(k); err == nil {
			t.Fatalf("key %q accepted", k)
		}
	}
}

func TestSpecValidate(t *testing.T) {
	pid := project.NewID()
	s := Spec{ProjectID: pid, Description: "  Reject expired access tokens  ", Constraints: []string{" ", "no new deps "}, Acceptance: []AcceptanceCriterion{{" "}, {"expired token -> 401"}}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.Kind != KindTask || s.Description != "Reject expired access tokens" || s.Outcome != s.Description {
		t.Fatalf("normalisation: %+v", s)
	}
	if len(s.Constraints) != 1 || len(s.Acceptance) != 1 {
		t.Fatalf("empty items must be dropped: %+v", s)
	}
	bad := []Spec{
		{Description: "x"},
		{ProjectID: pid, Description: "   "},
		{ProjectID: pid, Description: strings.Repeat("x", maxDescription+1)},
		{ProjectID: pid, Kind: "epic", Description: "x"},
		{ProjectID: pid, Key: "bad key", Description: "x"},
		{ProjectID: pid, Kind: KindGroup, Description: "g", Pins: []string{"x"}},
		{ProjectID: pid, Kind: KindGroup, Description: "g", Cohort: "c"},
		{ProjectID: pid, Description: "x", Verification: verification.Policy{Regression: []verification.CheckSpec{{ID: "a", Command: []string{"true"}}}}},
	}
	for i, b := range bad {
		if err := b.Validate(); !fault.Is(err, fault.CodeInvalidInput) {
			t.Errorf("case %d: %v", i, err)
		}
	}
	if w := (Spec{Outcome: "Reject expired tokens and add login page"}).Warnings(); len(w) != 2 {
		t.Fatalf("expected conjunction + no-checks warnings, got %v", w)
	}
}

func TestDerivePrecedence(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		f    Facts
		want Status
	}{
		{"fresh", Facts{Now: now}, StatusReady},
		{"group", Facts{Group: true}, StatusGroup},
		{"archived wins", Facts{Archived: true, Complete: true}, StatusArchived},
		{"blocked", Facts{UnmetRequires: 1}, StatusBlocked},
		{"claimed", Facts{LeaseActive: true}, StatusClaimed},
		{"claimed beats blocked", Facts{LeaseActive: true, UnmetRequires: 2}, StatusClaimed},
		{"verifying beats claimed", Facts{Verifying: true, LeaseActive: true}, StatusVerifying},
		{"pending beats claimed", Facts{SubmissionPending: true, LeaseActive: true}, StatusAwaitingVerification},
		{"interrupted", Facts{Interrupted: true}, StatusInterrupted},
		{"blocked beats interrupted", Facts{Interrupted: true, UnmetRequires: 1}, StatusBlocked},
		{"failed", Facts{VerificationFailed: true}, StatusVerificationFailed},
		{"exhausted beats failed", Facts{VerificationFailed: true, Exhausted: true}, StatusNeedsAttention},
		{"cooldown", Facts{Now: now, CooldownUntil: now.Add(time.Minute)}, StatusCooldown},
		{"cooldown expired", Facts{Now: now, CooldownUntil: now.Add(-time.Minute)}, StatusReady},
		{"cooldown beats failed", Facts{Now: now, VerificationFailed: true, CooldownUntil: now.Add(time.Minute)}, StatusCooldown},
		{"failed once cooldown expired", Facts{Now: now, VerificationFailed: true, CooldownUntil: now.Add(-time.Minute)}, StatusVerificationFailed},
		{"awaiting integration", Facts{AwaitingIntegration: true}, StatusAwaitingIntegration},
		{"complete", Facts{Complete: true, UnmetRequires: 3, LeaseActive: true}, StatusComplete},
	}
	for _, c := range cases {
		if got := Derive(c.f); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
	claimable := map[Status]bool{StatusReady: true, StatusInterrupted: true, StatusVerificationFailed: true}
	active := map[Status]bool{StatusClaimed: true, StatusVerifying: true, StatusAwaitingVerification: true, StatusAwaitingIntegration: true}
	all := []Status{StatusArchived, StatusComplete, StatusAwaitingIntegration, StatusVerifying, StatusAwaitingVerification, StatusClaimed, StatusBlocked, StatusNeedsAttention, StatusVerificationFailed, StatusCooldown, StatusInterrupted, StatusReady, StatusGroup}
	for _, s := range all {
		if s.Claimable() != claimable[s] || s.Active() != active[s] {
			t.Errorf("%s claimable=%v active=%v", s, s.Claimable(), s.Active())
		}
		if _, ok := ParseStatus(string(s)); !ok {
			t.Errorf("ParseStatus(%s)", s)
		}
	}
	// Progress glyphs: ● complete; ◐ started but not complete (every state
	// that implies an attempt, plus ready/blocked/needs_attention once an
	// attempt existed); ○ never started.
	if StatusComplete.Glyph() != "●" || StatusClaimed.Glyph() != "◐" || StatusVerifying.Glyph() != "◐" || StatusAwaitingVerification.Glyph() != "◐" || StatusAwaitingIntegration.Glyph() != "◐" || StatusVerificationFailed.Glyph() != "◐" || StatusCooldown.Glyph() != "◐" || StatusInterrupted.Glyph() != "◐" {
		t.Fatal("started glyphs")
	}
	if StatusReady.Glyph() != "○" || StatusBlocked.Glyph() != "○" || StatusNeedsAttention.Glyph() != "○" || Glyph(StatusReady, true) != "◐" || Glyph(StatusBlocked, true) != "◐" || Glyph(StatusNeedsAttention, true) != "◐" || Glyph(StatusArchived, true) != "○" || Glyph(StatusComplete, false) != "●" {
		t.Fatal("never-started glyphs")
	}
}
