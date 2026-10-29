package app_test

import (
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// A --project name shared by several projects must fail with
// INVALID_INPUT naming every match by id, and each listed id must resolve on
// retry.
func TestResolveProjectAmbiguousNameListsMatches(t *testing.T) {
	e, ctx := openBoard(t)
	left, err := e.AddProject(ctx, "Case Fold")
	if err != nil {
		t.Fatal(err)
	}
	right, err := e.AddProject(ctx, "case-fold")
	if err != nil {
		t.Fatal(err)
	}

	_, err = e.ResolveProject(ctx, "casefold")
	if !fault.Is(err, fault.CodeInvalidInput) {
		t.Fatalf("want INVALID_INPUT, got %v", err)
	}
	msg := err.Error()
	for _, p := range []struct{ id, name string }{{string(left.ID), left.Name}, {string(right.ID), right.Name}} {
		if want := p.id + " (" + p.name + ")"; !strings.Contains(msg, want) {
			t.Fatalf("error must list %q: %s", want, msg)
		}
		got, err := e.ResolveProject(ctx, p.id)
		if err != nil || string(got.ID) != p.id {
			t.Fatalf("retry with listed id %s: %v %+v", p.id, err, got)
		}
	}
}
