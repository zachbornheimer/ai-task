package cli

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/task"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		os.WriteFile(path, []byte(got), 0o644)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run with -update): %v\n--- got ---\n%s", path, err, got)
	}
	if string(want) != got {
		t.Fatalf("%s differs\n--- want ---\n%s--- got ---\n%s", name, want, got)
	}
}

func tv(id, title string, st task.Status) app.TaskView {
	return app.TaskView{ID: task.ID(id), Kind: task.KindTask, Title: title, Status: st, CreatedAt: time.Date(2026, 10, 9, 9, 12, 0, 0, time.UTC)}
}

// snapshot is the requester's Beads-style example rendered from view data.
func snapshot() app.PlanSnapshot {
	g1 := &app.Rel{ID: "at-g01", Title: "Identity Core", Status: task.StatusGroup}
	g2 := &app.Rel{ID: "at-g02", Title: "Embed Platform", Status: task.StatusGroup}
	f16 := tv("at-f16", "Verify end-to-end OAuth flow", task.StatusBlocked)
	f16.Group = g1
	f16.Requires = []app.Rel{{ID: "at-d83", Title: "Implement PostgreSQL token store", Status: task.StatusClaimed}, {ID: "at-e29", Title: "Implement scope enforcement", Status: task.StatusReady}}
	tasks := []app.TaskView{
		tv("at-a91", "Rotate leaked credentials", task.StatusReady),
		withGroup(tv("at-c17", "Confirm OAuth API compatibility", task.StatusComplete), g1),
		withGroup(tv("at-d83", "Implement PostgreSQL token store", task.StatusClaimed), g1),
		withGroup(tv("at-e29", "Implement scope enforcement", task.StatusReady), g1),
		f16,
		withGroup(tv("at-h14", "Build application shell", task.StatusReady), g2),
		withGroup(tv("at-j37", "Implement iframe messaging", task.StatusReady), g2),
	}
	return app.PlanSnapshot{Revision: 7, Tasks: tasks, Groups: []app.GroupView{
		{ID: "at-g01", Title: "Identity Core", Progress: app.Progress{Complete: 1, Total: 4}},
		{ID: "at-g02", Title: "Embed Platform", Progress: app.Progress{Complete: 0, Total: 2}},
	}}
}

func withGroup(v app.TaskView, g *app.Rel) app.TaskView { v.Group = g; return v }

func TestListGolden(t *testing.T) {
	var b bytes.Buffer
	renderList(&b, snapshot(), app.FilterOpen)
	golden(t, "list.txt", b.String())

	ready := snapshot()
	var only []app.TaskView
	for _, v := range ready.Tasks {
		if v.Status.Claimable() {
			only = append(only, v)
		}
	}
	ready.Tasks = only
	b.Reset()
	renderList(&b, ready, app.FilterReady)
	golden(t, "list_ready.txt", b.String())

	blocked := snapshot()
	blocked.Tasks = []app.TaskView{blocked.Tasks[4]}
	b.Reset()
	renderList(&b, blocked, app.FilterBlocked)
	golden(t, "list_blocked.txt", b.String())

	// Glyph rules: ● only for complete, ◐ for live claim or verification,
	// ○ otherwise with a bracketed annotation; a group is ● only when its
	// nonempty member set is complete; empty groups are (0/0) and ○.
	mixed := app.PlanSnapshot{Tasks: []app.TaskView{
		tv("at-v01", "Verifying now", task.StatusVerifying),
		tv("at-w01", "Waiting for cohort", task.StatusAwaitingVerification),
		tv("at-x01", "Failed last time", task.StatusVerificationFailed),
		tv("at-y01", "Done", task.StatusComplete),
	}, Groups: []app.GroupView{{ID: "at-g03", Title: "All done", Progress: app.Progress{Complete: 2, Total: 2}, Complete: true}, {ID: "at-g04", Title: "Empty", Progress: app.Progress{}}}}
	b.Reset()
	renderList(&b, mixed, app.FilterAll)
	golden(t, "list_glyphs.txt", b.String())
}

func TestShowGolden(t *testing.T) {
	v := tv("at-e29", "Implement scope enforcement", task.StatusReady)
	v.Project = "identity-service"
	v.Group = &app.Rel{ID: "at-g01", Title: "Identity Core", Status: task.StatusGroup}
	v.Outcome = "Requests cannot exercise scopes they were not granted."
	v.Acceptance = []task.AcceptanceCriterion{{Description: "Allowed scopes work."}, {Description: "Escalated scopes fail."}}
	v.Requires = []app.Rel{{ID: "at-c17", Title: "Confirm OAuth API compatibility", Status: task.StatusComplete}}
	v.Blocks = []app.Rel{{ID: "at-f16", Title: "Verify end-to-end OAuth flow", Status: task.StatusBlocked}}
	var b bytes.Buffer
	renderShow(&b, v, false)
	golden(t, "show.txt", b.String())

	// A claimed task with handoff and attempt details; no token anywhere.
	c := tv("at-d83", "Implement PostgreSQL token store", task.StatusClaimed)
	exp := time.Date(2026, 10, 9, 10, 12, 0, 0, time.UTC)
	c.Attempt = &app.AttemptView{Seq: 2, StartedAt: exp.Add(-time.Hour), LeaseExpiresAt: exp, LeaseActive: true, Workspace: "/ws/at-d83-2", Branch: "at/at-d83/2"}
	c.Handoff = &execution.Handoff{LatestNext: "wire the migration", Learnings: []string{"pool size matters"}, TotalLogs: 1, RecentLogs: []execution.RecordedLog{{Seq: 4, AttemptSeq: 1, At: exp.Add(-2 * time.Hour), LogEntry: execution.LogEntry{Done: "schema drafted", Next: "wire the migration", Learned: "pool size matters"}}}}
	b.Reset()
	renderShow(&b, c, false)
	golden(t, "show_claimed.txt", b.String())
	if bytes.Contains(b.Bytes(), []byte("sess-")) {
		t.Fatal("token rendered")
	}
}

func TestAckAndVerifyRendering(t *testing.T) {
	v := tv("at-b42", "Implement OAuth token endpoint", task.StatusBlocked)
	v.Group = &app.Rel{ID: "at-g01", Title: "Identity Core", Status: task.StatusGroup}
	v.Requires = []app.Rel{{ID: "at-a91", Status: task.StatusReady}}
	var b bytes.Buffer
	b.WriteString("✓ Created at-b42 · Implement OAuth token endpoint\n")
	renderAck(&b, v)
	golden(t, "ack.txt", b.String())

	b.Reset()
	renderVerify(&b, app.VerifyResult{TaskID: "at-b42", Mode: "complete", Passed: true, Completed: true, Revision: "abc123", Summary: "2/2 required checks passed", Message: "task complete; claim ended"})
	renderVerify(&b, app.VerifyResult{TaskID: "at-b42", Mode: "task", Passed: false, Status: task.StatusClaimed, Summary: "0/1 required checks passed; failed: unit (failed)", Message: "task checks failed; this run does not complete the task"})
	golden(t, "verify.txt", b.String())
}
