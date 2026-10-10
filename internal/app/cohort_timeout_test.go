package app_test

import (
	"strings"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// TestCohortTimeoutIsNotAStrike: a cohort member whose task check outruns
// its budget, or a shared regression suite that does, is reported as
// VERIFICATION_TIMEOUT and never counts against any member, like the
// single-task path.
func TestCohortTimeoutIsNotAStrike(t *testing.T) {
	submitPair := func(t *testing.T, f *fixture, e2e string) (task.ID, task.ID) {
		api, ui := cohortPair(f, e2e)
		sa, su := f.claim(string(api)), f.claim(string(ui))
		f.commit(sa.Workspace, "api.txt", "api")
		f.commit(su.Workspace, "ui.txt", "ui")
		f.e.Verify(f.ctx, sa.Token, verification.ModeComplete)
		f.e.Verify(f.ctx, su.Token, verification.ModeComplete)
		return api, ui
	}
	t.Run("member task check", func(t *testing.T) {
		f := newGitFixture(t)
		f.proj.Budgets = project.Budgets{Small: 300 * time.Millisecond, Medium: 5 * time.Second, Regression: 5 * time.Second}
		if err := f.e.UpdateProject(f.ctx, f.proj); err != nil {
			t.Fatal(err)
		}
		api, ui := submitPair(t, f, "sleep 1")
		v := f.show(string(api))
		if v.Failures != 0 || !strings.HasPrefix(v.LastError, "VERIFICATION_TIMEOUT") {
			t.Fatalf("timed-out member: failures=%d last_error=%q", v.Failures, v.LastError)
		}
		if u := f.show(string(ui)); u.Failures != 0 {
			t.Fatalf("peer charged: failures=%d last_error=%q", u.Failures, u.LastError)
		}
	})
	t.Run("shared regression", func(t *testing.T) {
		f := newGitFixture(t)
		f.setRegression(check("regress", "sleep 1", true))
		f.proj, _ = f.e.Project(f.ctx, f.proj.ID)
		f.proj.Budgets = project.Budgets{Small: 5 * time.Second, Medium: 5 * time.Second, Regression: 300 * time.Millisecond}
		if err := f.e.UpdateProject(f.ctx, f.proj); err != nil {
			t.Fatal(err)
		}
		api, ui := submitPair(t, f, "test -f api.txt && test -f ui.txt")
		for _, id := range []task.ID{api, ui} {
			v := f.show(string(id))
			if v.Failures != 0 || !strings.HasPrefix(v.LastError, "VERIFICATION_TIMEOUT") || !strings.Contains(v.LastError, "regress") {
				t.Fatalf("%s: failures=%d last_error=%q", id, v.Failures, v.LastError)
			}
		}
	})
}
