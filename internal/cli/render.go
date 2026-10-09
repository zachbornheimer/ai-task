package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// Rendering follows the Beads visual convention the requester supplied:
// ○ not in progress, ◐ in progress (live claim or verification), ● complete;
// bracketed annotations for states the glyph does not express; group
// hierarchy by indentation (organizational, never dependency order);
// hard prerequisites as a "requires:" line on blocked tasks.

func stateLabel(s task.Status) string {
	switch s {
	case task.StatusReady:
		return "OPEN"
	case task.StatusClaimed:
		return "IN PROGRESS"
	case task.StatusComplete:
		return "COMPLETE"
	}
	return strings.ToUpper(strings.ReplaceAll(string(s), "_", " "))
}

// line renders one compact list row.
func line(v app.TaskView) string {
	s := fmt.Sprintf("%s %s  %s", v.Status.Glyph(), v.ID, v.Title)
	if a := v.Status.Annotation(); a != "" {
		s += " " + a
	}
	return s
}

func groupLine(g app.GroupView) string {
	glyph := "○"
	if g.Complete {
		glyph = "●"
	}
	return fmt.Sprintf("%s %s  %s (%d/%d complete)", glyph, g.ID, g.Title, g.Progress.Complete, g.Progress.Total)
}

// renderList prints the hierarchy (list) or a flat filtered list.
func renderList(w io.Writer, snap app.PlanSnapshot, filter app.Filter) {
	if filter == app.FilterReady || filter == app.FilterBlocked || filter == app.FilterArchived {
		for _, t := range snap.Tasks {
			fmt.Fprintln(w, line(t))
			if filter == app.FilterBlocked {
				fmt.Fprintf(w, "    requires: %s\n", requiresText(t, true))
			}
		}
		return
	}
	byGroup := map[task.ID][]app.TaskView{}
	var top []app.TaskView
	for _, t := range snap.Tasks {
		if t.Group != nil {
			byGroup[t.Group.ID] = append(byGroup[t.Group.ID], t)
		} else {
			top = append(top, t)
		}
	}
	children := map[task.ID][]app.GroupView{}
	var roots []app.GroupView
	for _, g := range snap.Groups {
		if g.ParentID != "" {
			children[g.ParentID] = append(children[g.ParentID], g)
		} else {
			roots = append(roots, g)
		}
	}
	for _, t := range top {
		fmt.Fprintln(w, line(t))
		if t.Status == task.StatusBlocked {
			fmt.Fprintf(w, "    requires: %s\n", requiresText(t, false))
		}
	}
	for i, g := range roots {
		if i > 0 {
			fmt.Fprintln(w)
		}
		renderGroup(w, g, byGroup, children, "")
	}
}

func renderGroup(w io.Writer, g app.GroupView, byGroup map[task.ID][]app.TaskView, children map[task.ID][]app.GroupView, indent string) {
	fmt.Fprintln(w, indent+groupLine(g))
	type row struct {
		text   string
		extra  string
		isLast bool
	}
	var rows []row
	for _, t := range byGroup[g.ID] {
		r := row{text: line(t)}
		if t.Status == task.StatusBlocked {
			r.extra = "requires: " + requiresText(t, false)
		}
		rows = append(rows, r)
	}
	subs := children[g.ID]
	total := len(rows) + len(subs)
	for i, r := range rows {
		branch := "├── "
		if i == total-1 {
			branch = "└── "
		}
		fmt.Fprintln(w, indent+branch+r.text)
		if r.extra != "" {
			fmt.Fprintln(w, indent+"        "+r.extra)
		}
	}
	for i, sg := range subs {
		branch, next := "├── ", "│   "
		if len(rows)+i == total-1 {
			branch, next = "└── ", "    "
		}
		fmt.Fprint(w, indent+branch)
		renderGroup(w, sg, byGroup, children, indent+next)
	}
}

func requiresText(t app.TaskView, withState bool) string {
	var parts []string
	for _, r := range t.Requires {
		if r.Status == task.StatusComplete {
			continue
		}
		if withState {
			parts = append(parts, fmt.Sprintf("%s (%s)", r.ID, shortState(r.Status)))
		} else {
			parts = append(parts, string(r.ID))
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

// shortState is the parenthetical state used in requires: lines.
func shortState(s task.Status) string {
	switch s {
	case task.StatusReady:
		return "open"
	case task.StatusClaimed:
		return "claimed"
	}
	return strings.ReplaceAll(string(s), "_", " ")
}

// renderAck prints the short acknowledgement after a mutation.
func renderAck(w io.Writer, v app.TaskView) {
	if v.Group != nil {
		fmt.Fprintf(w, "Group:    %s\n", v.Group.Title)
	}
	if len(v.Requires) > 0 {
		var ids []string
		for _, r := range v.Requires {
			ids = append(ids, string(r.ID))
		}
		fmt.Fprintf(w, "Requires: %s\n", strings.Join(ids, ", "))
	}
	if v.Kind == task.KindGroup {
		fmt.Fprintf(w, "Members:  %d\n", len(v.Children))
		return
	}
	fmt.Fprintf(w, "State:    %s\n", capitalise(stateLabel(v.Status)))
}

func capitalise(s string) string {
	s = strings.ToLower(s)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// renderShow prints the annotated detail view.
func renderShow(w io.Writer, v app.TaskView, full bool) {
	fmt.Fprintf(w, "%s %s · %s\n", v.Status.Glyph(), v.ID, v.Title)
	fmt.Fprintf(w, "[%s]\n", stateLabel(v.Status))
	fmt.Fprintf(w, "Created: %s\n", v.CreatedAt.Format("2006-01-02 15:04"))
	if v.Project != "" {
		fmt.Fprintf(w, "Project: %s\n", v.Project)
	}
	if v.Key != "" {
		fmt.Fprintf(w, "Key: %s\n", v.Key)
	}
	if v.Group != nil {
		fmt.Fprintf(w, "Group: %s\n", v.Group.Title)
	}
	if v.Cohort != "" {
		fmt.Fprintf(w, "Cohort: %s\n", v.Cohort)
	}
	if v.Kind == task.KindGroup {
		if v.Progress != nil {
			fmt.Fprintf(w, "Progress: %d/%d complete\n", v.Progress.Complete, v.Progress.Total)
		}
		if len(v.Children) > 0 {
			fmt.Fprintln(w, "\nMEMBERS")
			for _, c := range v.Children {
				fmt.Fprintf(w, "%s %s %s\n", c.Status.Glyph(), c.ID, c.Title)
			}
		}
		return
	}
	fmt.Fprintf(w, "\nDESCRIPTION\n%s\n", v.Title)
	if v.Outcome != "" && v.Outcome != v.Title {
		fmt.Fprintf(w, "\nOUTCOME\n%s\n", v.Outcome)
	}
	if len(v.Constraints) > 0 {
		fmt.Fprintln(w, "\nCONSTRAINTS")
		for _, c := range v.Constraints {
			fmt.Fprintf(w, "• %s\n", c)
		}
	}
	if len(v.Acceptance) > 0 {
		fmt.Fprintln(w, "\nACCEPTANCE CRITERIA")
		for _, a := range v.Acceptance {
			fmt.Fprintf(w, "• %s\n", a.Description)
		}
	}
	if len(v.Requires) > 0 {
		fmt.Fprintln(w, "\nREQUIRES")
		for _, r := range v.Requires {
			fmt.Fprintf(w, "%s %s %s\n", r.Status.Glyph(), r.ID, r.Title)
		}
	}
	if len(v.Blocks) > 0 {
		fmt.Fprintln(w, "\nBLOCKS")
		for _, r := range v.Blocks {
			fmt.Fprintf(w, "%s %s %s\n", r.Status.Glyph(), r.ID, r.Title)
		}
	}
	if len(v.Checks) > 0 {
		fmt.Fprintln(w, "\nTASK CHECKS")
		for _, ch := range v.Checks {
			kind := "required"
			if !ch.Required {
				kind = "optional"
			}
			fmt.Fprintf(w, "• %s (%s): %s\n", ch.ID, kind, strings.Join(ch.Command, " "))
		}
	}
	fmt.Fprintln(w, "\nVERIFICATION")
	fmt.Fprintf(w, "Task:        %s\n", runText(v.Verification.Task))
	fmt.Fprintf(w, "Regression:  %s\n", runText(v.Verification.Regression))
	if v.Verification.Complete != nil {
		fmt.Fprintf(w, "Complete:    %s\n", runText(v.Verification.Complete))
	}
	if a := v.Attempt; a != nil {
		state := "lease expired"
		if a.LeaseActive {
			state = "lease active until " + a.LeaseExpiresAt.Format(time.RFC3339)
		} else if a.EndedAt != nil {
			state = string(a.EndReason)
		}
		fmt.Fprintf(w, "\nATTEMPT\n#%d (%s)\n", a.Seq, state)
		if a.Workspace != "" {
			fmt.Fprintf(w, "Workspace: %s (%s)\n", a.Workspace, a.Branch)
		}
	}
	if s := v.Submission; s != nil {
		fmt.Fprintf(w, "\nSUBMISSION\n#%d from attempt %d, revision %s, verification %s\n", s.ID, s.AttemptSeq, orDash(s.Revision), s.Verification)
	}
	if v.CompletedAt != nil {
		fmt.Fprintf(w, "\nCOMPLETED\n%s", v.CompletedAt.Format("2006-01-02 15:04"))
		if v.CompletedRevision != "" {
			fmt.Fprintf(w, " at revision %s", v.CompletedRevision)
		}
		fmt.Fprintln(w)
	}
	if v.AttentionReason != "" {
		fmt.Fprintf(w, "\nATTENTION\n%s\n", v.AttentionReason)
	}
	if v.LastError != "" {
		fmt.Fprintf(w, "\nLAST ERROR (not counted as a failure)\n%s\n", v.LastError)
	}
	if v.Failures > 0 {
		fmt.Fprintf(w, "\nRETRIES\n%d failed attempt(s)", v.Failures)
		if v.NextEligibleAt != nil {
			fmt.Fprintf(w, "; claimable after %s", v.NextEligibleAt.Format(time.RFC3339))
		}
		fmt.Fprintln(w)
	}
	if v.Handoff != nil && !v.Handoff.Empty() {
		fmt.Fprintln(w)
		renderHandoff(w, *v.Handoff)
	}
	fmt.Fprintf(w, "\nHISTORY\n%s — Task defined.\n", v.CreatedAt.Format("15:04"))
	if full && v.History != nil {
		for _, a := range v.History.Attempts {
			end := "open"
			if a.EndedAt != nil {
				end = string(a.EndReason)
			}
			fmt.Fprintf(w, "%s — Attempt #%d started (%s).\n", a.StartedAt.Format("15:04"), a.Seq, end)
		}
		for _, l := range v.History.Logs {
			renderLog(w, l)
		}
		for _, r := range v.History.Runs {
			fmt.Fprintf(w, "run %d %s %s %s\n", r.ID, r.Mode, r.Status, r.Summary)
			renderEvidence(w, r.Evidence, true)
		}
		for _, i := range v.History.Integrations {
			fmt.Fprintf(w, "%s — Promoted %s to %s as %s.\n", i.CreatedAt.Format("15:04"), short(i.SourceRevision), i.TargetBranch, short(i.ResultRevision))
		}
	}
}

func runText(r *app.RunView) string {
	if r == nil {
		return "not run"
	}
	when := ""
	if r.FinishedAt != nil {
		when = " at " + r.FinishedAt.Format("15:04")
	}
	return fmt.Sprintf("%s%s (run %d)", r.Status, when, r.ID)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

func renderHandoff(w io.Writer, h execution.Handoff) {
	if h.Empty() {
		return
	}
	fmt.Fprintln(w, "HANDOFF")
	if h.LatestNext != "" {
		fmt.Fprintf(w, "Next:     %s\n", h.LatestNext)
	}
	for _, l := range h.Learnings {
		fmt.Fprintf(w, "Learned:  %s\n", l)
	}
	for _, l := range h.Inherited {
		ref := string(l.TaskID)
		if l.Key != "" {
			ref = l.Key
		}
		fmt.Fprintf(w, "From %s: %s\n", ref, l.Learned)
	}
	if f := h.LastFailure; f != nil {
		fmt.Fprintf(w, "Last failure (%s run #%d): %s\n", f.Mode, f.RunID, f.Summary)
		for _, c := range f.Checks {
			fmt.Fprintf(w, "  ✗ %s (%s)", c.CheckID, c.Outcome)
			if c.Message != "" {
				fmt.Fprintf(w, ": %s", c.Message)
			}
			fmt.Fprintln(w)
			for _, tail := range []string{c.StderrTail, c.StdoutTail} {
				for _, line := range strings.Split(strings.TrimRight(tail, "\n"), "\n") {
					if line != "" {
						fmt.Fprintf(w, "      %s\n", line)
					}
				}
			}
		}
	}
	if h.TotalLogs > 0 {
		fmt.Fprintf(w, "Recent log (%d of %d, newest first):\n", len(h.RecentLogs), h.TotalLogs)
		for _, l := range h.RecentLogs {
			renderLog(w, l)
		}
	}
	for _, wn := range h.Warnings {
		fmt.Fprintf(w, "Warning:  %s\n", wn)
	}
}

// renderWarnings prints planning warnings keyed by task reference.
func renderWarnings(w io.Writer, warnings map[string][]string) {
	keys := make([]string, 0, len(warnings))
	for k := range warnings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, msg := range warnings[k] {
			fmt.Fprintf(w, "Warning:  %s: %s\n", k, msg)
		}
	}
}

func renderLog(w io.Writer, l execution.RecordedLog) {
	fmt.Fprintf(w, "  #%d attempt %d %s\n", l.Seq, l.AttemptSeq, l.At.Format(time.RFC3339))
	for _, kv := range []struct{ k, v string }{{"done", l.Done}, {"next", l.Next}, {"learned", l.Learned}, {"note", l.Note}} {
		if kv.v != "" {
			fmt.Fprintf(w, "    %-8s %s\n", kv.k+":", kv.v)
		}
	}
}

func renderEvidence(w io.Writer, evidence []verification.Evidence, full bool) {
	for _, ev := range evidence {
		kind := "required"
		if !ev.Required {
			kind = "optional"
		}
		fmt.Fprintf(w, "  %-20s %-8s %s exit=%d %s\n", ev.CheckID, ev.Outcome, kind, ev.ExitCode, ev.FinishedAt.Sub(ev.StartedAt).Round(time.Millisecond))
		if ev.Message != "" {
			fmt.Fprintf(w, "      %s\n", ev.Message)
		}
		if ev.Outcome != verification.OutcomePassed || full {
			names := []string{"stderr", "stdout"}
			outs := map[string]string{"stdout": ev.Stdout, "stderr": ev.Stderr}
			sort.Strings(names)
			for _, name := range names {
				out := strings.TrimSpace(outs[name])
				if out == "" {
					continue
				}
				if !full && len(out) > 2000 {
					out = "... " + out[len(out)-2000:]
				}
				fmt.Fprintf(w, "      --- %s ---\n", name)
				for _, l := range strings.Split(out, "\n") {
					fmt.Fprintf(w, "      %s\n", l)
				}
			}
		}
	}
}

func renderVerify(w io.Writer, res app.VerifyResult) {
	outcome := "FAILED"
	if res.Passed {
		outcome = "passed"
	}
	fmt.Fprintf(w, "✓ Verify %s · %s: %s\n", res.Mode, res.TaskID, outcome)
	if res.Revision != "" {
		fmt.Fprintf(w, "Revision:  %s\n", res.Revision)
	}
	if res.IntegratedRevision != "" {
		fmt.Fprintf(w, "Promoted:  %s\n", res.IntegratedRevision)
	}
	if res.Summary != "" {
		fmt.Fprintf(w, "Summary:   %s\n", res.Summary)
	}
	switch {
	case res.Completed:
		fmt.Fprintln(w, "Task:      COMPLETE (claim ended)")
	case res.Submitted:
		fmt.Fprintf(w, "Task:      %s (submitted for cohort verification; claim ended)\n", stateLabel(res.Status))
	default:
		fmt.Fprintf(w, "Task:      NOT complete (%s)\n", strings.ToLower(stateLabel(res.Status)))
	}
	if res.Message != "" {
		fmt.Fprintln(w, res.Message)
	}
	renderEvidence(w, res.Evidence, false)
}

func renderSummary(w io.Writer, s app.Summary) {
	fmt.Fprintf(w, "Project %s · plan rev %d · %d tasks, %d open, %d claimable, %d active\n", s.ProjectID, s.PlanRev, s.Total, s.Open, s.Claimable, s.Active)
	var keys []string
	for st := range s.Counts {
		keys = append(keys, string(st))
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "  %-22s %d\n", k, s.Counts[task.Status(k)])
	}
	for _, c := range s.PendingCohorts {
		fmt.Fprintf(w, "  cohort %q ready for verification\n", c)
	}
	switch {
	case s.Done:
		fmt.Fprintln(w, "● done: every task is complete")
	case s.Stalled:
		fmt.Fprintln(w, "○ stalled: nothing can proceed without intervention")
		for _, r := range s.Reasons {
			fmt.Fprintf(w, "  - %s\n", r)
		}
	}
}
