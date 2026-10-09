package app_test

// Tests for first-class dependency handling and machine-only graph
// execution: edges at creation, discovered blockers, release, manual
// gates, and the driver summary.

import (
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/task"
)

func TestAddWithDependenciesIsAtomic(t *testing.T) {
	f := newFixture(t)
	a := f.add("a")
	b, _, err := f.e.AddWithDependencies(f.ctx, task.Spec{ProjectID: f.proj.ID, Description: "b"}, []task.ID{a.ID}, nil)
	if err != nil || f.status(b.ID) != task.StatusBlocked {
		t.Fatalf("%v %s", err, f.status(b.ID))
	}
	// --blocks: the new task becomes a prerequisite of an existing one.
	x, _, err := f.e.AddWithDependencies(f.ctx, task.Spec{ProjectID: f.proj.ID, Description: "x"}, nil, []task.ID{a.ID})
	if err != nil || f.status(a.ID) != task.StatusBlocked || f.status(x.ID) != task.StatusAvailable {
		t.Fatalf("%v a=%s x=%s", err, f.status(a.ID), f.status(x.ID))
	}
	// requires + blocks that would form a cycle creates nothing.
	before, _ := f.e.List(f.ctx, f.proj.ID, app.ListFilter{})
	_, _, err = f.e.AddWithDependencies(f.ctx, task.Spec{ProjectID: f.proj.ID, Description: "cyc"}, []task.ID{b.ID}, []task.ID{a.ID})
	wantCode(t, err, fault.CodeDependencyCycle)
	after, _ := f.e.List(f.ctx, f.proj.ID, app.ListFilter{})
	if len(after) != len(before) {
		t.Fatal("task created despite invalid edge")
	}
	_, _, err = f.e.AddWithDependencies(f.ctx, task.Spec{ProjectID: f.proj.ID, Description: "missing"}, []task.ID{"task-0000000000000000"}, nil)
	wantCode(t, err, fault.CodeNotFound)
}

func TestDiscoveredBlockerReleaseAndResume(t *testing.T) {
	f := newFixture(t)
	a := f.add("a")
	s, err := f.e.Take(f.ctx, app.TakeRequest{Task: &a.ID})
	if err != nil {
		t.Fatal(err)
	}
	// Mid-work the agent finds a prerequisite, records it as a task that
	// blocks a, and hands a back.
	p, _, err := f.e.AddWithDependencies(f.ctx, task.Spec{ProjectID: f.proj.ID, Description: "prereq"}, nil, []task.ID{a.ID})
	if err != nil {
		t.Fatal(err)
	}
	if f.status(a.ID) != task.StatusInProgress {
		t.Fatal("active attempt stays visible while blocked edge is added")
	}
	f.e.Log(f.ctx, s.Token, execution.LogEntry{Done: "half", Next: "finish after prereq"})
	v, err := f.e.Release(f.ctx, s.Token, "blocked on prereq")
	if err != nil || v.Status != task.StatusBlocked {
		t.Fatalf("%+v %v", v, err)
	}
	_, err = f.e.Log(f.ctx, s.Token, execution.LogEntry{Done: "late"})
	wantCode(t, err, fault.CodeSessionSuperseded)
	// Automatic take now picks the prerequisite, not the blocked task.
	s2, err := f.e.Take(f.ctx, app.TakeRequest{Project: f.proj.ID})
	if err != nil || s2.TaskID != p.ID {
		t.Fatalf("%+v %v", s2, err)
	}
	if _, err := f.e.Finish(f.ctx, s2.Token, app.FinishOptions{}); err != nil {
		t.Fatal(err)
	}
	// a is available again with its handoff intact and the release note.
	s3, err := f.e.Take(f.ctx, app.TakeRequest{Project: f.proj.ID})
	if err != nil || s3.TaskID != a.ID || s3.AttemptSeq != 2 || s3.Handoff.LatestNext != "finish after prereq" || s3.Handoff.TotalLogs != 2 {
		t.Fatalf("%+v %v", s3, err)
	}
	h, _ := f.e.History(f.ctx, a.ID, 10, 0)
	if h.Attempts[0].EndReason != execution.EndReleased || h.Entries[1].Note != "blocked on prereq" {
		t.Fatalf("%+v", h)
	}
	// Releasing a finished or unknown session fails cleanly.
	_, err = f.e.Release(f.ctx, s2.Token, "")
	wantCode(t, err, fault.CodeSessionFinished)
	_, err = f.e.Release(f.ctx, execution.NewToken(), "")
	wantCode(t, err, fault.CodeInvalidSession)
}

func TestManualTasksGateMachinesAndSummaryReportsStuck(t *testing.T) {
	f := newFixture(t)
	gate, _, err := f.e.Add(f.ctx, task.Spec{ProjectID: f.proj.ID, Description: "approve API key", Manual: true})
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := f.e.AddWithDependencies(f.ctx, task.Spec{ProjectID: f.proj.ID, Description: "use the key"}, []task.ID{gate.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing a machine can take: the only takeable task is manual.
	_, err = f.e.Take(f.ctx, app.TakeRequest{Project: f.proj.ID})
	wantCode(t, err, fault.CodeNoAvailableTask)
	sum, err := f.e.Summary(f.ctx, f.proj.ID)
	if err != nil || !sum.Stuck || sum.Done || sum.ManualPending != 1 || sum.Takeable != 0 || sum.Open != 2 {
		t.Fatalf("%+v %v", sum, err)
	}
	avail, _ := f.e.Available(f.ctx, f.proj.ID)
	if len(avail) != 1 || !avail[0].Manual {
		t.Fatalf("manual task must still be listed as available for humans: %+v", avail)
	}
	// A human takes it explicitly and finishes.
	s, err := f.e.Take(f.ctx, app.TakeRequest{Task: &gate.ID})
	if err != nil {
		t.Fatal(err)
	}
	sum, _ = f.e.Summary(f.ctx, f.proj.ID)
	if sum.Stuck || sum.Active != 1 {
		t.Fatalf("in-flight manual work is not stuck: %+v", sum)
	}
	if _, err := f.e.Finish(f.ctx, s.Token, app.FinishOptions{}); err != nil {
		t.Fatal(err)
	}
	s2, err := f.e.Take(f.ctx, app.TakeRequest{Project: f.proj.ID})
	if err != nil || s2.TaskID != work.ID {
		t.Fatalf("%+v %v", s2, err)
	}
	if _, err := f.e.Finish(f.ctx, s2.Token, app.FinishOptions{}); err != nil {
		t.Fatal(err)
	}
	sum, _ = f.e.Summary(f.ctx, f.proj.ID)
	if !sum.Done || sum.Open != 0 || sum.Counts[task.StatusComplete] != 2 {
		t.Fatalf("%+v", sum)
	}
}

// A machine-only driver loop over a diamond graph: every task is executed
// exactly once, in a valid order, by competing workers, and the summary
// reports done at the end and never stuck in between.
func TestMachineOnlyGraphExecution(t *testing.T) {
	f := newFixture(t)
	a := f.add("a")
	b, _, _ := f.e.AddWithDependencies(f.ctx, task.Spec{ProjectID: f.proj.ID, Description: "b"}, []task.ID{a.ID}, nil)
	c, _, _ := f.e.AddWithDependencies(f.ctx, task.Spec{ProjectID: f.proj.ID, Description: "c"}, []task.ID{a.ID}, nil)
	d, _, _ := f.e.AddWithDependencies(f.ctx, task.Spec{ProjectID: f.proj.ID, Description: "d"}, []task.ID{b.ID, c.ID}, nil)
	order := []task.ID{}
	for i := 0; i < 20; i++ { // a driver loop; several workers would share it
		sum, err := f.e.Summary(f.ctx, f.proj.ID)
		if err != nil {
			t.Fatal(err)
		}
		if sum.Done {
			break
		}
		if sum.Stuck {
			t.Fatalf("stuck mid-execution: %+v", sum)
		}
		s, err := f.open().Take(f.ctx, app.TakeRequest{Project: f.proj.ID, Lease: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		order = append(order, s.TaskID)
		if _, err := f.e.Finish(f.ctx, s.Token, app.FinishOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(order) != 4 || order[0] != a.ID || order[3] != d.ID {
		t.Fatalf("order %v", order)
	}
	sum, _ := f.e.Summary(f.ctx, f.proj.ID)
	if !sum.Done {
		t.Fatalf("%+v", sum)
	}
}
