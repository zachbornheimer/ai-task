package app_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// A retry cooldown is one rule everywhere: the task is not claimable
// (list, status, automatic and explicit claim agree), a waiting claim
// waits for it instead of reporting a stall, and it becomes claimable
// when the clock passes the cooldown without anyone writing.
func TestCooldownIsOneRuleEverywhere(t *testing.T) {
	for _, via := range []string{"failed verification", "release --failed"} {
		via := via
		t.Run(via, func(t *testing.T) {
			f := newGitFixture(t)
			p, _ := f.e.Project(f.ctx, f.proj.ID)
			p.RetryCooldown = time.Hour
			if err := f.e.UpdateProject(f.ctx, p); err != nil {
				t.Fatal(err)
			}
			var a task.ID
			if via == "failed verification" {
				a = f.addWith("a", "false")
			} else {
				a = f.add("a")
			}
			s := f.claim(string(a))
			f.commit(s.Workspace, "a.txt", "a")
			if via == "failed verification" {
				_, err := f.e.Verify(f.ctx, s.Token, verification.ModeComplete)
				wantCode(t, err, fault.CodeVerificationFailed)
				if _, err := f.e.Release(f.ctx, s.Token, app.ReleaseOptions{}); err != nil {
					t.Fatal(err)
				}
			} else if _, err := f.e.Release(f.ctx, s.Token, app.ReleaseOptions{Failed: true}); err != nil {
				t.Fatal(err)
			}
			if st := f.status(string(a)); st != task.StatusCooldown {
				t.Fatalf("status %s", st)
			}
			snap, _ := f.e.List(f.ctx, app.ListQuery{ProjectID: f.proj.ID, Filter: app.FilterReady})
			if len(snap.Tasks) != 0 {
				t.Fatal("cooling task listed as ready")
			}
			sum, _ := f.e.Summary(f.ctx, f.proj.ID)
			if sum.Claimable != 0 || sum.Stalled || sum.Cooling != 1 || sum.NextEligibleAt == nil {
				t.Fatalf("summary: %+v", sum)
			}
			_, err := f.e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID})
			wantCode(t, err, fault.CodeNoAvailableTask)
			_, err = f.e.Claim(f.ctx, app.ClaimRequest{TaskID: &a})
			wantCode(t, err, fault.CodeNoAvailableTask)
			// A waiting claim waits (here: until its context ends), never STALLED.
			ctx, cancel := context.WithTimeout(f.ctx, 300*time.Millisecond)
			_, err = f.e.Claim(ctx, app.ClaimRequest{ProjectID: f.proj.ID, Wait: true})
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("waiting claim during cooldown: %v", err)
			}
			// Time passes, nothing is written: the task is claimable again.
			f.c.Advance(time.Hour + time.Minute)
			if st := f.status(string(a)); !st.Claimable() {
				t.Fatalf("after cooldown: %s", st)
			}
			s2, err := f.e.Claim(f.ctx, app.ClaimRequest{ProjectID: f.proj.ID, Wait: true})
			if err != nil || s2.Task.ID != a {
				t.Fatalf("%+v %v", s2, err)
			}
		})
	}
}

// Decision table: for every state the engine can produce, the SQL
// claim prefilter and the derived status agree on claimability, and the
// candidate the engine would hand out is one of them.
func TestClaimablePrefilterMatchesDerivedStatus(t *testing.T) {
	f := newGitFixture(t)
	p, _ := f.e.Project(f.ctx, f.proj.ID)
	p.RetryCooldown = time.Hour
	p.MaxAttempts = 2
	if err := f.e.UpdateProject(f.ctx, p); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// ready
	f.add("ready")
	// blocked
	f.add("blocked", "ready")
	// claimed (live lease)
	f.claim(string(f.add("claimed")))
	// interrupted (lease expired, never ended)
	interrupted := f.add("interrupted")
	f.claim(string(interrupted))
	// verification_failed, cooldown expired later; and one still cooling
	failed := f.addWith("failed", "false")
	sf := f.claim(string(failed))
	f.commit(sf.Workspace, "f.txt", "f")
	if _, err := f.e.Verify(f.ctx, sf.Token, verification.ModeComplete); fault.CodeOf(err) != fault.CodeVerificationFailed {
		t.Fatal(err)
	}
	f.e.Release(f.ctx, sf.Token, app.ReleaseOptions{})
	// exhausted (max attempts 2)
	exhausted := f.add("exhausted")
	for i := 0; i < 2; i++ {
		s := f.claim(string(exhausted))
		f.e.Release(f.ctx, s.Token, app.ReleaseOptions{Failed: true})
		f.c.Advance(time.Hour + time.Minute)
	}
	// complete
	f.complete(f.claim(string(f.add("complete"))))
	// archived
	archived := f.add("archived")
	f.apply(plan.ArchiveTask{Target: plan.Ref(archived), Reason: "test"})
	// cohort member awaiting verification
	cm := f.apply(plan.AddTask{Key: "cm", Title: "cm", Cohort: "c", TaskChecks: okChecks()}, plan.AddTask{Key: "cm2", Title: "cm2", Cohort: "c", TaskChecks: okChecks()}).Created["cm"]
	scm := f.claim(string(cm))
	f.commit(scm.Workspace, "cm.txt", "cm")
	if _, err := f.e.Verify(f.ctx, scm.Token, verification.ModeComplete); err != nil {
		t.Fatal(err)
	}
	// unverifiable (legacy policy) and workspace-blocked, via direct SQL
	unverifiable := f.add("unverifiable")
	opt, _ := json.Marshal(verification.Policy{TaskChecks: []verification.CheckSpec{check("o", "true", false)}})
	if _, err := db.Exec(`UPDATE tasks SET policy_json = ? WHERE id = ?`, string(opt), unverifiable); err != nil {
		t.Fatal(err)
	}
	wsblocked := f.add("wsblocked")
	if _, err := db.Exec(`UPDATE tasks SET workspace_error = 'broken' WHERE id = ?`, wsblocked); err != nil {
		t.Fatal(err)
	}
	// Interrupted: expire the lease of the "interrupted" task only (the
	// clock already moved for the exhausted one; re-claim to get a live lease
	// for "claimed").
	f.c.Advance(execution.DefaultLease + time.Minute)
	f.claim("claimed")
	// cooling: released --failed after every clock advance, so its cooldown
	// is still running when the table is read
	cooling := f.add("cooling")
	sc := f.claim(string(cooling))
	f.e.Release(f.ctx, sc.Token, app.ReleaseOptions{Failed: true})
	// failed proof whose cooldown is still running: the case where the
	// derived status and the SQL prefilter used to disagree
	fc := f.addWith("failedcooling", "false")
	sfc := f.claim(string(fc))
	f.commit(sfc.Workspace, "fc.txt", "fc")
	if _, err := f.e.Verify(f.ctx, sfc.Token, verification.ModeComplete); fault.CodeOf(err) != fault.CodeVerificationFailed {
		t.Fatal(err)
	}
	f.e.Release(f.ctx, sfc.Token, app.ReleaseOptions{})

	store, err := sqlite.Open(f.ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	proj, _ := f.e.Project(f.ctx, f.proj.ID)
	now := f.c.Now()
	err = store.Read(f.ctx, func(tx *sqlite.Tx) error {
		all, err := tx.ListRecords(f.proj.ID, sqlite.ScopeAll, now, proj.MaxAttempts)
		if err != nil {
			return err
		}
		sqlClaimable, err := tx.ListRecords(f.proj.ID, sqlite.ScopeClaimable, now, proj.MaxAttempts)
		if err != nil {
			return err
		}
		inSQL := map[task.ID]bool{}
		for _, r := range sqlClaimable {
			inSQL[r.Task.ID] = true
		}
		seen := map[task.Status]bool{}
		for _, r := range all {
			st := r.Status(now, proj.MaxAttempts)
			seen[st] = true
			if st.Claimable() != inSQL[r.Task.ID] {
				t.Errorf("%s (%s, status %s): derived claimable=%v, SQL prefilter=%v", r.Task.Key, r.Task.ID, st, st.Claimable(), inSQL[r.Task.ID])
			}
		}
		for _, want := range []task.Status{task.StatusReady, task.StatusBlocked, task.StatusClaimed, task.StatusInterrupted, task.StatusVerificationFailed, task.StatusCooldown, task.StatusNeedsAttention, task.StatusComplete, task.StatusArchived, task.StatusAwaitingVerification} {
			if !seen[want] {
				t.Errorf("decision table missing status %s", want)
			}
		}
		cand, ok, err := tx.ClaimCandidate(f.proj.ID, now, proj.MaxAttempts)
		if err != nil || !ok || !cand.Status(now, proj.MaxAttempts).Claimable() {
			t.Errorf("candidate: ok=%v %v", ok, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
