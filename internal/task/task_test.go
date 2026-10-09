package task

import (
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
)

func TestIDs(t *testing.T) {
	id := NewID()
	if len(id) != len("task-")+16 {
		t.Fatalf("bad length %q", id)
	}
	if _, err := ParseID(string(id)); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseID("proj-0123456789abcdef"); !fault.Is(err, fault.CodeInvalidInput) {
		t.Fatal("wrong prefix accepted")
	}
	seen := map[ID]bool{}
	for i := 0; i < 1000; i++ {
		n := NewID()
		if seen[n] {
			t.Fatal("collision in 1000 draws is astronomically unlikely; RNG broken")
		}
		seen[n] = true
	}
}

func TestSpecValidate(t *testing.T) {
	pid := project.NewID()
	s := Spec{ProjectID: pid, Description: "  Reject expired access tokens  ", Constraints: []string{" ", "no new deps "}, Acceptance: []AcceptanceCriterion{{" "}, {"expired token -> 401"}}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.Description != "Reject expired access tokens" || s.Outcome != s.Description {
		t.Fatalf("normalisation: %+v", s)
	}
	if len(s.Constraints) != 1 || s.Constraints[0] != "no new deps" || len(s.Acceptance) != 1 {
		t.Fatalf("empty items must be dropped: %+v", s)
	}
	bad := []Spec{
		{Description: "x"},
		{ProjectID: pid, Description: "   "},
		{ProjectID: pid, Description: strings.Repeat("x", maxDescription+1)},
		{ProjectID: pid, Description: "x", Constraints: []string{strings.Repeat("c", maxConstraint+1)}},
		{ProjectID: pid, Description: "x", Acceptance: make([]AcceptanceCriterion, maxListItems+1)},
	}
	for i, b := range bad {
		if err := b.Validate(); !fault.Is(err, fault.CodeInvalidInput) {
			t.Errorf("case %d: %v", i, err)
		}
	}
}

func TestWarnings(t *testing.T) {
	if w := (Spec{Outcome: "Reject expired tokens"}).Warnings(); len(w) != 0 {
		t.Fatalf("unexpected warnings %v", w)
	}
	if w := (Spec{Outcome: "Reject expired tokens and add login page"}).Warnings(); len(w) != 1 {
		t.Fatalf("expected conjunction warning, got %v", w)
	}
}

func TestDerivePrecedence(t *testing.T) {
	cases := []struct {
		name string
		f    Facts
		want Status
	}{
		{"fresh", Facts{}, StatusAvailable},
		{"blocked", Facts{UnmetDependencies: 1}, StatusBlocked},
		{"leased", Facts{LeaseActive: true}, StatusInProgress},
		{"leased beats blocked", Facts{LeaseActive: true, UnmetDependencies: 2}, StatusInProgress},
		{"interrupted", Facts{Interrupted: true}, StatusInterrupted},
		{"blocked beats interrupted", Facts{Interrupted: true, UnmetDependencies: 1}, StatusBlocked},
		{"pending", Facts{SubmissionPending: true}, StatusAwaitingVerification},
		{"pending beats lease", Facts{SubmissionPending: true, LeaseActive: true}, StatusAwaitingVerification},
		{"failed", Facts{VerificationFailed: true}, StatusVerificationFailed},
		{"failed with new lease shows attempt", Facts{VerificationFailed: true, LeaseActive: true}, StatusInProgress},
		{"blocked beats failed", Facts{VerificationFailed: true, UnmetDependencies: 1}, StatusBlocked},
		{"awaiting integration", Facts{AwaitingIntegration: true}, StatusAwaitingIntegration},
		{"complete wins", Facts{Complete: true, UnmetDependencies: 3, LeaseActive: true}, StatusComplete},
	}
	for _, c := range cases {
		if got := Derive(c.f); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
	takeable := map[Status]bool{StatusAvailable: true, StatusInterrupted: true, StatusVerificationFailed: true}
	for _, s := range []Status{StatusBlocked, StatusAvailable, StatusInProgress, StatusInterrupted, StatusAwaitingVerification, StatusVerificationFailed, StatusAwaitingIntegration, StatusComplete} {
		if s.Takeable() != takeable[s] {
			t.Errorf("%s takeable = %v", s, s.Takeable())
		}
		if _, ok := ParseStatus(string(s)); !ok {
			t.Errorf("ParseStatus(%s) failed", s)
		}
	}
	if StatusInterrupted.TakePriority() >= StatusVerificationFailed.TakePriority() || StatusVerificationFailed.TakePriority() >= StatusAvailable.TakePriority() {
		t.Fatal("take priority order")
	}
}
