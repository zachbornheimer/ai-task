package app_test

import (
	"context"
	"reflect"
	"testing"

	at "github.com/zachbornheimer/ai-task"
	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/task"
)

func TestReleaseFailedCountsAttempt(t *testing.T) {
	f := newFixture(t)
	a := f.add("a")

	s := f.claim(string(a))
	if err := f.e.Release(f.ctx, app.ReleaseRequest{Token: s.Token, Note: "plain"}); err != nil {
		t.Fatal(err)
	}
	if v := f.show(string(a)); v.Failures != 0 || v.Status != task.StatusReady {
		t.Fatalf("plain release: failures=%d status=%s", v.Failures, v.Status)
	}

	s = f.claim(string(a))
	if err := f.e.Release(f.ctx, app.ReleaseRequest{Token: s.Token, Failed: true}); err != nil {
		t.Fatal(err)
	}
	if v := f.show(string(a)); v.Failures != 1 || v.Status != task.StatusReady {
		t.Fatalf("failed release: failures=%d status=%s", v.Failures, v.Status)
	}
}

func TestEngineReleaseSignature(t *testing.T) {
	m, ok := reflect.TypeFor[at.Engine]().MethodByName("Release")
	if !ok {
		t.Fatal("Engine has no Release")
	}
	want := reflect.TypeOf(func(context.Context, at.ReleaseRequest) error { return nil })
	if m.Type != want {
		t.Fatalf("Release is %v, want %v", m.Type, want)
	}
}
