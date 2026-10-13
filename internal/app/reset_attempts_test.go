package app_test

import (
	"strings"
	"testing"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/plan"
)

func TestResetAttemptsPlanner(t *testing.T) {
	f := newFixture(t)
	id := f.add("x")
	s := f.claim(string(id))

	_, err := f.e.Apply(f.ctx, plan.ChangeSet{
		ProjectID:  f.proj.ID,
		Session:    string(s.Token),
		Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(id), ResetAttempts: true}},
	})
	wantCode(t, err, fault.CodePlanConflict)

	if _, err := f.e.Apply(f.ctx, plan.ChangeSet{
		ProjectID:  f.proj.ID,
		Planner:    true,
		Operations: []plan.Change{plan.UpdateTask{Target: plan.Ref(id), ResetAttempts: true}},
	}); err != nil {
		t.Fatal(err)
	}
	v, err := f.e.Show(f.ctx, f.proj.ID, string(id), true)
	if err != nil {
		t.Fatal(err)
	}
	if !plannerResetInHistory(v.History, v.Handoff) {
		t.Fatalf("planner reset not in history: history=%+v handoff=%+v", v.History, v.Handoff)
	}
}

func plannerResetInHistory(h *app.History, handoff *execution.Handoff) bool {
	var logs []execution.RecordedLog
	if h != nil {
		logs = append(logs, h.Logs...)
	}
	if handoff != nil {
		logs = append(logs, handoff.RecentLogs...)
	}
	for _, l := range logs {
		note := strings.ToLower(l.Note)
		if strings.Contains(note, "planner") && strings.Contains(note, "reset") {
			return true
		}
	}
	return false
}
