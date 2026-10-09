package app_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/dependency"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// clock is a settable clock shared by every engine in a test.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)} }
func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fixture struct {
	t    *testing.T
	ctx  context.Context
	e    *app.Engine
	c    *clock
	path string
	proj project.Project
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), c: newClock(), path: filepath.Join(t.TempDir(), "tasks.db")}
	f.e = f.open()
	var err error
	f.proj, err = f.e.InitProject(f.ctx, "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// open opens another engine on the same database, simulating a separate CLI
// process (separate connection pool, same file).
func (f *fixture) open() *app.Engine {
	e, err := app.Open(f.ctx, app.Config{Path: f.path, Now: f.c.Now})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { e.Close() })
	return e
}

func (f *fixture) add(desc string) task.Task {
	f.t.Helper()
	tk, _, err := f.e.Add(f.ctx, task.Spec{ProjectID: f.proj.ID, Description: desc})
	if err != nil {
		f.t.Fatal(err)
	}
	return tk
}

func (f *fixture) status(id task.ID) task.Status {
	f.t.Helper()
	v, err := f.e.Show(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return v.Status
}

func wantCode(t *testing.T, err error, code fault.Code) {
	t.Helper()
	if fault.CodeOf(err) != code {
		t.Fatalf("got error %v (code %q), want %q", err, fault.CodeOf(err), code)
	}
}

func TestLifecycleDependencyReleasesOnCompletion(t *testing.T) {
	f := newFixture(t)
	a := f.add("Parse tokens")
	b := f.add("Reject expired access tokens")
	if err := f.e.AddDependency(f.ctx, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if f.status(a.ID) != task.StatusAvailable || f.status(b.ID) != task.StatusBlocked {
		t.Fatalf("initial statuses: %s %s", f.status(a.ID), f.status(b.ID))
	}
	avail, err := f.e.Available(f.ctx, f.proj.ID)
	if err != nil || len(avail) != 1 || avail[0].ID != a.ID {
		t.Fatalf("available = %+v, %v", avail, err)
	}
	_, err = f.e.Take(f.ctx, app.TakeRequest{Task: &b.ID})
	wantCode(t, err, fault.CodeTaskBlocked)

	s, err := f.e.Take(f.ctx, app.TakeRequest{Project: f.proj.ID})
	if err != nil {
		t.Fatal(err)
	}
	if s.TaskID != a.ID || s.AttemptSeq != 1 || s.Resumed || s.Token == "" {
		t.Fatalf("session: %+v", s)
	}
	if f.status(a.ID) != task.StatusInProgress {
		t.Fatalf("after take: %s", f.status(a.ID))
	}
	_, err = f.e.Take(f.ctx, app.TakeRequest{Task: &a.ID})
	wantCode(t, err, fault.CodeTaskAlreadyTaken)
	_, err = f.e.Take(f.ctx, app.TakeRequest{Project: f.proj.ID})
	wantCode(t, err, fault.CodeNoAvailableTask)

	if _, err := f.e.Log(f.ctx, s.Token, execution.LogEntry{Done: "parsed", Learned: "tokens are base64url"}); err != nil {
		t.Fatal(err)
	}
	exp, err := f.e.Renew(f.ctx, s.Token, 2*time.Hour)
	if err != nil || !exp.Equal(f.c.Now().Add(2*time.Hour)) {
		t.Fatalf("renew: %v %v", exp, err)
	}
	res, err := f.e.Finish(f.ctx, s.Token, app.FinishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != task.StatusComplete || res.Verification != "passed" || res.SubmissionID == 0 {
		t.Fatalf("finish: %+v", res)
	}
	// Idempotent replay.
	again, err := f.e.Finish(f.ctx, s.Token, app.FinishOptions{})
	if err != nil || !again.AlreadySubmitted || again.SubmissionID != res.SubmissionID {
		t.Fatalf("replay: %+v %v", again, err)
	}
	// Finished session has no further authority.
	_, err = f.e.Log(f.ctx, s.Token, execution.LogEntry{Note: "late"})
	wantCode(t, err, fault.CodeSessionFinished)

	if f.status(a.ID) != task.StatusComplete || f.status(b.ID) != task.StatusAvailable {
		t.Fatalf("after completion: %s %s", f.status(a.ID), f.status(b.ID))
	}
	_, err = f.e.Take(f.ctx, app.TakeRequest{Task: &a.ID})
	wantCode(t, err, fault.CodeTaskComplete)
	v, err := f.e.Show(f.ctx, b.ID)
	if err != nil || len(v.Requires) != 1 || v.Requires[0].Status != task.StatusComplete {
		t.Fatalf("show b: %+v %v", v, err)
	}
	h, err := f.e.History(f.ctx, a.ID, 10, 0)
	if err != nil || h.Total != 1 || len(h.Attempts) != 1 || len(h.Submissions) != 1 {
		t.Fatalf("history: %+v %v", h, err)
	}
}

func TestCrashAndResumeFromAnotherProcess(t *testing.T) {
	f := newFixture(t)
	a := f.add("Reject expired access tokens")
	s1, err := f.e.Take(f.ctx, app.TakeRequest{Task: &a.ID, Lease: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []execution.LogEntry{
		{Done: "Updated token validation", Next: "Test malformed expiry values", Learned: "Expiry validation is shared by two middleware paths"},
		{Note: "Watch for backward compatibility"},
	} {
		if _, err := f.e.Log(f.ctx, s1.Token, e); err != nil {
			t.Fatal(err)
		}
	}
	// Agent 1 crashes. Nothing is cleaned up; the lease simply ages out.
	f.c.Advance(31 * time.Minute)
	if f.status(a.ID) != task.StatusInterrupted {
		t.Fatalf("status after expiry: %s", f.status(a.ID))
	}
	_, err = f.e.Log(f.ctx, s1.Token, execution.LogEntry{Done: "late write"})
	wantCode(t, err, fault.CodeLeaseExpired)
	_, err = f.e.Renew(f.ctx, s1.Token, 0)
	wantCode(t, err, fault.CodeLeaseExpired)
	_, err = f.e.Finish(f.ctx, s1.Token, app.FinishOptions{})
	wantCode(t, err, fault.CodeLeaseExpired)

	// A second process resumes via automatic selection.
	e2 := f.open()
	s2, err := e2.Take(f.ctx, app.TakeRequest{Project: f.proj.ID})
	if err != nil {
		t.Fatal(err)
	}
	if s2.TaskID != a.ID || s2.AttemptSeq != 2 || !s2.Resumed {
		t.Fatalf("resume session: %+v", s2)
	}
	h := s2.Handoff
	if h.LatestNext != "Test malformed expiry values" || len(h.Learnings) != 1 || h.TotalLogs != 2 || len(h.RecentLogs) != 2 {
		t.Fatalf("handoff: %+v", h)
	}
	if h.RecentLogs[0].Note != "Watch for backward compatibility" || h.RecentLogs[0].AttemptSeq != 1 {
		t.Fatalf("recent logs newest-first with attempt: %+v", h.RecentLogs)
	}
	// Old token is fenced out even though its lease could be "renewed".
	_, err = f.e.Log(f.ctx, s1.Token, execution.LogEntry{Done: "zombie"})
	wantCode(t, err, fault.CodeSessionSuperseded)
	_, err = f.e.Finish(f.ctx, s1.Token, app.FinishOptions{})
	wantCode(t, err, fault.CodeSessionSuperseded)
	// New token works from either process.
	if _, err := f.e.Log(f.ctx, s2.Token, execution.LogEntry{Done: "resumed"}); err != nil {
		t.Fatal(err)
	}
	hist, err := e2.History(f.ctx, a.ID, 100, 0)
	if err != nil || hist.Total != 3 || hist.Entries[2].AttemptSeq != 2 || len(hist.Attempts) != 2 {
		t.Fatalf("history: %+v %v", hist, err)
	}
	if hist.Attempts[0].EndReason != execution.EndSuperseded {
		t.Fatalf("first attempt should be superseded: %+v", hist.Attempts[0])
	}
}

func TestConcurrentTakeOfOneTaskYieldsOneAuthority(t *testing.T) {
	f := newFixture(t)
	a := f.add("contended")
	const n = 100
	var wins, taken, other int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		e := f.e
		if i%2 == 1 {
			e = f.open() // half the claimants use separate connection pools
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := e.Take(f.ctx, app.TakeRequest{Task: &a.ID})
			switch {
			case err == nil:
				atomic.AddInt32(&wins, 1)
			case fault.Is(err, fault.CodeTaskAlreadyTaken):
				atomic.AddInt32(&taken, 1)
			default:
				atomic.AddInt32(&other, 1)
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins != 1 || taken != n-1 || other != 0 {
		t.Fatalf("wins=%d taken=%d other=%d", wins, taken, other)
	}
	h, _ := f.e.History(f.ctx, a.ID, 1, 0)
	if len(h.Attempts) != 1 {
		t.Fatalf("exactly one attempt must exist, got %d", len(h.Attempts))
	}
}

func TestConcurrentAutomaticTakeAssignsEachTaskOnce(t *testing.T) {
	f := newFixture(t)
	const tasks, agents = 20, 40
	ids := map[task.ID]bool{}
	for i := 0; i < tasks; i++ {
		ids[f.add("work").ID] = false
	}
	var mu sync.Mutex
	got := map[task.ID]int{}
	var none int32
	var wg sync.WaitGroup
	for i := 0; i < agents; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := f.open().Take(f.ctx, app.TakeRequest{Project: f.proj.ID})
			if err != nil {
				if fault.Is(err, fault.CodeNoAvailableTask) {
					atomic.AddInt32(&none, 1)
					return
				}
				t.Error(err)
				return
			}
			mu.Lock()
			got[s.TaskID]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(got) != tasks || int(none) != agents-tasks {
		t.Fatalf("assigned %d distinct tasks, %d agents got none", len(got), none)
	}
	for id, n := range got {
		if n != 1 {
			t.Fatalf("task %s claimed %d times", id, n)
		}
	}
}

func TestConcurrentLoggingAcrossTasks(t *testing.T) {
	f := newFixture(t)
	const tasks, entries = 8, 25
	var sessions []app.Session
	for i := 0; i < tasks; i++ {
		id := f.add("t").ID
		s, err := f.e.Take(f.ctx, app.TakeRequest{Task: &id})
		if err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, s)
	}
	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func(s app.Session) {
			defer wg.Done()
			e := f.open()
			for i := 0; i < entries; i++ {
				if _, err := e.Log(f.ctx, s.Token, execution.LogEntry{Done: "step"}); err != nil {
					t.Error(err)
					return
				}
			}
		}(s)
	}
	wg.Wait()
	for _, s := range sessions {
		h, err := f.e.History(f.ctx, s.TaskID, 1, 0)
		if err != nil || h.Total != entries {
			t.Fatalf("task %s has %d entries (%v)", s.TaskID, h.Total, err)
		}
	}
}

func TestDependencyInvariants(t *testing.T) {
	f := newFixture(t)
	a, b, c := f.add("a"), f.add("b"), f.add("c")
	wantCode(t, f.e.AddDependency(f.ctx, a.ID, a.ID), fault.CodeSelfDependency)
	if err := f.e.AddDependency(f.ctx, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	wantCode(t, f.e.AddDependency(f.ctx, b.ID, a.ID), fault.CodeDuplicateDependency)
	if err := f.e.AddDependency(f.ctx, c.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	wantCode(t, f.e.AddDependency(f.ctx, a.ID, c.ID), fault.CodeDependencyCycle)
	wantCode(t, f.e.AddDependency(f.ctx, a.ID, b.ID), fault.CodeDependencyCycle)
	wantCode(t, f.e.AddDependency(f.ctx, a.ID, "task-0000000000000000"), fault.CodeNotFound)

	other, err := f.e.InitProject(f.ctx, "other", "")
	if err != nil {
		t.Fatal(err)
	}
	x, _, err := f.e.Add(f.ctx, task.Spec{ProjectID: other.ID, Description: "x"})
	if err != nil {
		t.Fatal(err)
	}
	wantCode(t, f.e.AddDependency(f.ctx, x.ID, a.ID), fault.CodeCrossProject)

	// Batch atomicity: a batch containing a cycle adds nothing.
	d := f.add("d")
	err = f.e.AddDependencies(f.ctx, []dependency.Edge{{Task: d.ID, Requires: c.ID}, {Task: a.ID, Requires: d.ID}})
	wantCode(t, err, fault.CodeDependencyCycle)
	reqs, _, err := f.e.Dependencies(f.ctx, d.ID)
	if err != nil || len(reqs) != 0 {
		t.Fatalf("batch must be atomic: %+v %v", reqs, err)
	}

	g, err := f.e.Graph(f.ctx, f.proj.ID)
	if err != nil || len(g.Nodes) != 4 || len(g.Edges) != 2 {
		t.Fatalf("graph: %+v %v", g, err)
	}
	order, err := g.TopologicalOrder()
	if err != nil {
		t.Fatal(err)
	}
	pos := map[task.ID]int{}
	for i, id := range order {
		pos[id] = i
	}
	if !(pos[a.ID] < pos[b.ID] && pos[b.ID] < pos[c.ID]) {
		t.Fatalf("topo order violates edges: %v", order)
	}
	// Removing an edge recalculates eligibility immediately.
	if err := f.e.RemoveDependency(f.ctx, c.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if f.status(c.ID) != task.StatusAvailable {
		t.Fatalf("c should be available after edge removal: %s", f.status(c.ID))
	}
	wantCode(t, f.e.RemoveDependency(f.ctx, c.ID, b.ID), fault.CodeNotFound)
}

func TestConcurrentEdgeAddsCannotFormCycle(t *testing.T) {
	f := newFixture(t)
	const rounds = 30
	for r := 0; r < rounds; r++ {
		a, b, c := f.add("a"), f.add("b"), f.add("c")
		if err := f.e.AddDependency(f.ctx, b.ID, a.ID); err != nil {
			t.Fatal(err)
		}
		// Racing: c requires b (fine) and a requires c (closes a cycle iff
		// the first lands). At most one of the two cycle-closing orders can
		// succeed, and the stored graph must stay acyclic either way.
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); errs[0] = f.open().AddDependency(f.ctx, c.ID, b.ID) }()
		go func() { defer wg.Done(); errs[1] = f.open().AddDependency(f.ctx, a.ID, c.ID) }()
		wg.Wait()
		g, err := f.e.Graph(f.ctx, f.proj.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.TopologicalOrder(); err != nil {
			t.Fatalf("round %d: stored graph has a cycle (errs=%v)", r, errs)
		}
		for _, e := range errs {
			if e != nil && !fault.Is(e, fault.CodeDependencyCycle) {
				t.Fatalf("unexpected error: %v", e)
			}
		}
	}
}

func TestFinishWithRequiredChecksAwaitsVerification(t *testing.T) {
	f := newFixture(t)
	spec := task.Spec{ProjectID: f.proj.ID, Description: "needs tests", Verification: verification.Policy{
		TaskChecks: []verification.CheckSpec{{ID: "unit", Command: []string{"go", "test", "./..."}, Required: true}},
	}}
	a, _, err := f.e.Add(f.ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	b := f.add("dependent")
	if err := f.e.AddDependency(f.ctx, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	s, err := f.e.Take(f.ctx, app.TakeRequest{Task: &a.ID})
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.e.Finish(f.ctx, s.Token, app.FinishOptions{NoVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != task.StatusAwaitingVerification || res.Verification != "pending" || res.PolicyDigest != spec.Verification.Digest() {
		t.Fatalf("finish with checks: %+v", res)
	}
	// Agent's claim does not release the dependent, and the task cannot be
	// re-taken while its submission is pending.
	if f.status(b.ID) != task.StatusBlocked {
		t.Fatalf("dependent must stay blocked: %s", f.status(b.ID))
	}
	_, err = f.e.Take(f.ctx, app.TakeRequest{Task: &a.ID})
	wantCode(t, err, fault.CodeTaskAwaitingVerification)
	avail, _ := f.e.Available(f.ctx, f.proj.ID)
	if len(avail) != 0 {
		t.Fatalf("nothing should be available: %+v", avail)
	}
}

func TestProjectRegressionMergesIntoSubmission(t *testing.T) {
	f := newFixture(t)
	// A project-level regression check makes even a check-less task await
	// verification.
	p := f.proj
	p.Regression = []verification.CheckSpec{{ID: "full", Command: []string{"make", "test"}, Required: true}}
	if err := f.e.UpdateProject(f.ctx, p); err != nil {
		t.Fatal(err)
	}
	a := f.add("plain")
	s, err := f.e.Take(f.ctx, app.TakeRequest{Task: &a.ID})
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.e.Finish(f.ctx, s.Token, app.FinishOptions{NoVerify: true})
	if err != nil || res.Status != task.StatusAwaitingVerification {
		t.Fatalf("%+v %v", res, err)
	}
	if res.PolicyDigest != verification.Merge(verification.Policy{}, p.Regression).Digest() {
		t.Fatal("submission must be bound to the merged policy digest")
	}
}

func TestInvalidTokensAndInputs(t *testing.T) {
	f := newFixture(t)
	_, err := f.e.Log(f.ctx, execution.NewToken(), execution.LogEntry{Done: "x"})
	wantCode(t, err, fault.CodeInvalidSession)
	_, err = f.e.Finish(f.ctx, execution.NewToken(), app.FinishOptions{})
	wantCode(t, err, fault.CodeInvalidSession)
	a := f.add("a")
	s, _ := f.e.Take(f.ctx, app.TakeRequest{Task: &a.ID})
	_, err = f.e.Log(f.ctx, s.Token, execution.LogEntry{})
	wantCode(t, err, fault.CodeInvalidInput)
	_, err = f.e.Renew(f.ctx, s.Token, 10*time.Millisecond)
	wantCode(t, err, fault.CodeInvalidInput)
	_, err = f.e.Take(f.ctx, app.TakeRequest{Project: f.proj.ID, Lease: 48 * time.Hour})
	wantCode(t, err, fault.CodeInvalidInput)
	_, err = f.e.Take(f.ctx, app.TakeRequest{})
	wantCode(t, err, fault.CodeNoProject)
	_, _, err = f.e.Add(f.ctx, task.Spec{ProjectID: "proj-0000000000000000", Description: "x"})
	wantCode(t, err, fault.CodeNotFound)
	_, err = f.e.Show(f.ctx, "task-0000000000000000")
	wantCode(t, err, fault.CodeNotFound)
	// Tokens never appear in read models.
	v, _ := f.e.Show(f.ctx, a.ID)
	if v.Attempt == nil || v.Attempt.Seq != 1 {
		t.Fatalf("attempt view: %+v", v.Attempt)
	}
}

func TestLogsSurviveEngineClose(t *testing.T) {
	f := newFixture(t)
	a := f.add("durable")
	e1 := f.open()
	s, err := e1.Take(f.ctx, app.TakeRequest{Task: &a.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e1.Log(f.ctx, s.Token, execution.LogEntry{Done: "committed"}); err != nil {
		t.Fatal(err)
	}
	e1.Close() // simulate process exit immediately after acknowledgement
	h, err := f.open().History(f.ctx, a.ID, 10, 0)
	if err != nil || h.Total != 1 || h.Entries[0].Done != "committed" {
		t.Fatalf("log lost: %+v %v", h, err)
	}
}

func TestProjectResolutionAcrossWorktrees(t *testing.T) {
	f := newFixture(t)
	base, _ := project.CanonicalRoot(t.TempDir())
	repo := filepath.Join(base, "repo")
	os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755)
	os.MkdirAll(filepath.Join(repo, "pkg"), 0o755)
	linked := filepath.Join(base, "repo.wt")
	os.MkdirAll(linked, 0o755)
	os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644)

	p, err := f.e.InitProject(f.ctx, "", filepath.Join(repo, "pkg"))
	if err != nil || p.RootPath != repo || p.Name != "repo" {
		t.Fatalf("init: %+v %v", p, err)
	}
	for _, dir := range []string{repo, filepath.Join(repo, "pkg"), linked} {
		got, err := f.e.ResolveProject(f.ctx, "", dir)
		if err != nil || got.ID != p.ID {
			t.Fatalf("resolve from %s: %+v %v", dir, got, err)
		}
	}
	if got, err := f.e.ResolveProject(f.ctx, string(p.ID), ""); err != nil || got.ID != p.ID {
		t.Fatalf("by id: %v", err)
	}
	if got, err := f.e.ResolveProject(f.ctx, "repo", ""); err != nil || got.ID != p.ID {
		t.Fatalf("by name: %v", err)
	}
	_, err = f.e.ResolveProject(f.ctx, "", base)
	wantCode(t, err, fault.CodeNoProject)
	_, err = f.e.ResolveProject(f.ctx, "nope", "")
	wantCode(t, err, fault.CodeNotFound)
	// Plain (non-git) nested directory resolves by longest registered root.
	plain := filepath.Join(base, "plain")
	os.MkdirAll(filepath.Join(plain, "deep"), 0o755)
	pp, err := f.e.InitProject(f.ctx, "plain", plain)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.e.ResolveProject(f.ctx, "", filepath.Join(plain, "deep")); err != nil || got.ID != pp.ID {
		t.Fatalf("prefix resolve: %v", err)
	}
	// Double registration is rejected.
	_, err = f.e.InitProject(f.ctx, "again", repo)
	wantCode(t, err, fault.CodeInvalidInput)
}

func TestListFilters(t *testing.T) {
	f := newFixture(t)
	a, b := f.add("a"), f.add("b")
	f.e.AddDependency(f.ctx, b.ID, a.ID)
	s, _ := f.e.Take(f.ctx, app.TakeRequest{Task: &a.ID})
	blocked, _ := f.e.List(f.ctx, f.proj.ID, app.ListFilter{Statuses: []task.Status{task.StatusBlocked}})
	inprog, _ := f.e.List(f.ctx, f.proj.ID, app.ListFilter{Statuses: []task.Status{task.StatusInProgress}})
	all, _ := f.e.List(f.ctx, f.proj.ID, app.ListFilter{})
	if len(blocked) != 1 || blocked[0].ID != b.ID || len(inprog) != 1 || inprog[0].ID != a.ID || len(all) != 2 {
		t.Fatalf("filters: %+v %+v %+v", blocked, inprog, all)
	}
	if inprog[0].LeaseExpiresAt == nil || !inprog[0].LeaseExpiresAt.Equal(s.ExpiresAt) {
		t.Fatalf("lease in summary: %+v", inprog[0])
	}
	f.c.Advance(2 * time.Hour)
	interrupted, _ := f.e.List(f.ctx, f.proj.ID, app.ListFilter{Statuses: []task.Status{task.StatusInterrupted}})
	avail, _ := f.e.Available(f.ctx, f.proj.ID)
	if len(interrupted) != 1 || len(avail) != 0 {
		t.Fatalf("interrupted=%+v avail=%+v", interrupted, avail)
	}
}
