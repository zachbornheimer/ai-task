package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/proof"
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
func line(v app.TaskView, p painter) string {
	s := fmt.Sprintf("%s%s %s  %s", p.glyph(v.Status, v.Started), checkChangeMark(v), v.ID, v.Title)
	if a := v.Status.Annotation(); a != "" {
		s += " " + a
	}
	if v.Project != "" {
		s += p.dim("  [project: " + v.Project + "]")
	}
	return s
}

// checkChangeMark follows the status glyph of a task whose checks changed
// after it was first claimed.
func checkChangeMark(v app.TaskView) string {
	if len(v.CheckHistory) == 0 {
		return ""
	}
	return task.CheckChangedGlyph
}

func groupGlyph(g app.GroupView) string {
	switch {
	case g.Complete:
		return "●"
	case g.Progress.Started > 0 || g.Progress.Complete > 0:
		return "◐"
	}
	return "○"
}

// groupLine: ● when every executable member is complete (and there is
// one), ◐ when any member was ever started or completed, ○ otherwise.
func groupLine(g app.GroupView, p painter) string {
	s := fmt.Sprintf("%s %s  %s (%d/%d complete)", p.glyphRaw(groupGlyph(g)), g.ID, g.Title, g.Progress.Complete, g.Progress.Total)
	if gv := g.Verification; gv != nil && gv.Status != "" && gv.Status != "passed" {
		s += fmt.Sprintf(" [epic checks %s]", gv.Status)
	}
	if g.ArchivedAt != nil {
		s += " [archived]"
	}
	return s
}

func branch(p painter, s string) string { return p.dim(s) }

// renderList prints the hierarchy (list) or a flat filtered list.
func renderList(w io.Writer, snap app.PlanSnapshot, filter app.Filter, p painter) {
	if filter == app.FilterReady || filter == app.FilterBlocked || filter == app.FilterDraft || filter == app.FilterArchived || filter == app.FilterComplete {
		for i := range snap.Tasks {
			t := &snap.Tasks[i]
			sayln(w, line(*t, p))
			if filter == app.FilterBlocked {
				say(w, "    requires: %s\n", requiresText(*t, true))
			}
		}
		return
	}
	known := map[task.ID]bool{}
	for i := range snap.Groups {
		g := &snap.Groups[i]
		known[g.ID] = true
	}
	byGroup := map[task.ID][]app.TaskView{}
	var top []app.TaskView
	for i := range snap.Tasks {
		t := &snap.Tasks[i]
		// A member whose group is not in this listing (archived group,
		// filtered-out group) is shown at the top level, never hidden.
		if t.Group != nil && known[t.Group.ID] {
			byGroup[t.Group.ID] = append(byGroup[t.Group.ID], *t)
		} else {
			top = append(top, *t)
		}
	}
	children := map[task.ID][]app.GroupView{}
	var roots []app.GroupView
	for i := range snap.Groups {
		g := &snap.Groups[i]
		if g.ParentID != "" {
			children[g.ParentID] = append(children[g.ParentID], *g)
		} else {
			roots = append(roots, *g)
		}
	}
	for i := range top {
		t := &top[i]
		sayln(w, line(*t, p))
		if t.Status == task.StatusBlocked {
			say(w, "    requires: %s\n", requiresText(*t, false))
		}
	}
	for i := range roots {
		g := &roots[i]
		if i > 0 {
			sayln(w)
		}
		renderGroup(w, *g, byGroup, children, "", "", p)
	}
}

// renderGroup prints a group line prefixed by linePrefix (the tree branch
// of a nested group) and its members indented by indent.
func renderGroup(w io.Writer, g app.GroupView, byGroup map[task.ID][]app.TaskView, children map[task.ID][]app.GroupView, linePrefix, indent string, p painter) {
	sayln(w, linePrefix+groupLine(g, p))
	type row struct {
		text  string
		extra string
	}
	var rows []row
	for i := range byGroup[g.ID] {
		t := &byGroup[g.ID][i]
		r := row{text: line(*t, p)}
		if t.Status == task.StatusBlocked {
			r.extra = "requires: " + requiresText(*t, false)
		}
		rows = append(rows, r)
	}
	subs := children[g.ID]
	total := len(rows) + len(subs)
	for i, r := range rows {
		b := "├── "
		if i == total-1 {
			b = "└── "
		}
		sayln(w, indent+branch(p, b)+r.text)
		if r.extra != "" {
			sayln(w, indent+"        "+r.extra)
		}
	}
	for i := range subs {
		sg := &subs[i]
		b, next := "├── ", "│   "
		if len(rows)+i == total-1 {
			b, next = "└── ", "    "
		}
		renderGroup(w, *sg, byGroup, children, indent+branch(p, b), indent+branch(p, next), p)
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
		say(w, "Group:    %s\n", v.Group.Title)
	}
	if len(v.Requires) > 0 {
		var ids []string
		for _, r := range v.Requires {
			ids = append(ids, string(r.ID))
		}
		say(w, "Requires: %s\n", strings.Join(ids, ", "))
	}
	if v.Kind == task.KindGroup {
		say(w, "Members:  %d\n", len(v.Children))
		return
	}
	say(w, "State:    %s\n", capitalise(stateLabel(v.Status)))
}

func capitalise(s string) string {
	s = strings.ToLower(s)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func kindLabel(k task.Kind) string {
	if k == task.KindGroup {
		return "group"
	}
	return "task"
}

func showKindTag(k task.Kind) string {
	if k == task.KindGroup {
		return "GROUP"
	}
	return "TASK"
}

func updatedAt(v app.TaskView) time.Time {
	if !v.UpdatedAt.IsZero() {
		return v.UpdatedAt
	}
	return v.CreatedAt
}

func writeWrapped(w io.Writer, s string) {
	for line := range strings.SplitSeq(s, "\n") {
		say(w, "  %s\n", line)
	}
}

// renderShow prints the annotated detail view.
func renderShow(w io.Writer, v app.TaskView, full bool, p painter) {
	say(w, "%s %s [%s] · %s   [%s]\n", p.glyph(v.Status, v.Started), v.ID, showKindTag(v.Kind), v.Title, stateLabel(v.Status))
	if v.Creator != "" {
		say(w, "Created by: %s · Type: %s\n", v.Creator, kindLabel(v.Kind))
	} else {
		say(w, "Type: %s\n", kindLabel(v.Kind))
	}
	say(w, "Created: %s · Updated: %s\n", v.CreatedAt.Format("2006-01-02"), updatedAt(v).Format("2006-01-02"))
	if v.Project != "" {
		say(w, "Project: %s\n", v.Project)
	}
	if v.Key != "" {
		say(w, "Key: %s\n", v.Key)
	}
	if v.Kind == task.KindGroup {
		if v.Progress != nil {
			say(w, "Progress: %d/%d complete\n", v.Progress.Complete, v.Progress.Total)
		}
		say(w, "\nDESCRIPTION\n")
		writeWrapped(w, v.Title)
		if v.Outcome != "" && v.Outcome != v.Title {
			sayln(w, "\nOUTCOME")
			writeWrapped(w, v.Outcome)
		}
		if len(v.Checks) > 0 {
			say(w, "\nEPIC CHECKS (run on the target once every member is complete)\n")
			for _, ch := range v.Checks {
				say(w, "  • %s: %s\n", ch.ID, app.CheckText(ch))
			}
			if gv := v.GroupVerification; gv != nil {
				status := gv.Status
				if status == "" {
					status = "waiting for members"
				}
				say(w, "Verification: %s", status)
				if gv.Summary != "" {
					say(w, " (%s)", gv.Summary)
				}
				sayln(w)
			}
			if v.LastError != "" {
				say(w, "Last error: %s\n", v.LastError)
			}
		}
		if len(v.Children) > 0 {
			sayln(w, "\nCHILDREN")
			for _, c := range v.Children {
				say(w, "  ↳ %s %s: %s\n", p.glyph(c.Status, false), c.ID, c.Title)
			}
		}
		return
	}
	sayln(w, "\nDESCRIPTION")
	writeWrapped(w, v.Title)
	if v.Outcome != "" && v.Outcome != v.Title {
		sayln(w, "\nOUTCOME")
		writeWrapped(w, v.Outcome)
	}
	if len(v.Constraints) > 0 {
		sayln(w, "\nCONSTRAINTS")
		for _, c := range v.Constraints {
			say(w, "  • %s\n", c)
		}
	}
	if len(v.Acceptance) > 0 {
		sayln(w, "\nACCEPTANCE CRITERIA")
		for _, a := range v.Acceptance {
			say(w, "  • %s\n", a.Description)
		}
	}
	if v.Group != nil {
		sayln(w, "\nPARENT")
		say(w, "  ↑ %s %s: %s\n", p.glyph(v.Group.Status, false), v.Group.ID, v.Group.Title)
	}
	if len(v.Requires) > 0 {
		sayln(w, "\nREQUIRES")
		for _, r := range v.Requires {
			say(w, "  %s %s: %s\n", p.glyph(r.Status, false), r.ID, r.Title)
		}
	}
	if len(v.Blocks) > 0 {
		sayln(w, "\nBLOCKS")
		for _, r := range v.Blocks {
			say(w, "  ↳ %s %s: %s\n", p.glyph(r.Status, false), r.ID, r.Title)
		}
	}
	if v.Proof != nil || v.Regression != nil {
		sayln(w, "\nTASK CHECKS")
		writeProofLine(w, "proof", v.Proof)
		writeProofLine(w, "regression", v.Regression)
	} else if len(v.Checks) > 0 {
		sayln(w, "\nTASK CHECKS")
		for _, ch := range v.Checks {
			kind := "required"
			if !ch.Required {
				kind = "optional"
			}
			say(w, "  • %s (%s): %s\n", ch.ID, kind, app.CheckText(ch))
		}
	}
	renderCheckHistory(w, v.CheckHistory)
	sayln(w, "\nVERIFICATION")
	say(w, "  Task:        %s\n", runText(v.Verification.Task))
	say(w, "  Regression:  %s\n", runText(v.Verification.Regression))
	if v.Verification.Complete != nil {
		say(w, "  Complete:    %s\n", runText(v.Verification.Complete))
	}
	if a := v.Attempt; a != nil {
		state := "lease expired"
		if a.LeaseActive {
			state = "lease active until " + a.LeaseExpiresAt.Format(time.RFC3339)
		} else if a.EndedAt != nil {
			state = string(a.EndReason)
		}
		say(w, "\nATTEMPT\n  #%d (%s)\n", a.Seq, state)
		if a.Workspace != "" {
			say(w, "  Workspace: %s (%s)\n", a.Workspace, a.Branch)
		}
	}
	if s := v.Submission; s != nil {
		say(w, "\nSUBMISSION\n  #%d from attempt %d, revision %s, verification %s\n", s.ID, s.AttemptSeq, orDash(s.Revision), s.Verification)
	}
	if v.CompletedAt != nil {
		say(w, "\nCOMPLETED\n  %s", v.CompletedAt.Format("2006-01-02 15:04"))
		if v.CompletedRevision != "" {
			say(w, " at revision %s", v.CompletedRevision)
		}
		sayln(w)
	}
	if v.AttentionReason != "" {
		say(w, "\nATTENTION\n  %s\n", v.AttentionReason)
	}
	if v.LastError != "" {
		say(w, "\nLAST ERROR (not counted as a failure)\n  %s\n", v.LastError)
	}
	if v.Failures > 0 {
		say(w, "\nRETRIES\n  %d failed attempt(s)", v.Failures)
		if v.NextEligibleAt != nil {
			say(w, "; claimable after %s", v.NextEligibleAt.Format(time.RFC3339))
		}
		sayln(w)
	}
	if v.Handoff != nil && !v.Handoff.Empty() {
		sayln(w)
		renderHandoff(w, *v.Handoff)
	}
	renderComments(w, v)
	if full && v.History != nil {
		sayln(w, "\nHISTORY")
		for _, a := range v.History.Attempts {
			end := "open"
			if a.EndedAt != nil {
				end = string(a.EndReason)
			}
			say(w, "  %s — Attempt #%d started (%s).\n", a.StartedAt.Format("15:04"), a.Seq, end)
		}
		for i := range v.History.Runs {
			r := &v.History.Runs[i]
			say(w, "  run %d %s %s %s\n", r.ID, r.Mode, r.Status, r.Summary)
			renderEvidence(w, r.Evidence, true)
		}
		renderRunResults(w, v.RunResults)
		renderActions(w, v.Actions)
	}
}

// renderRunResults prints one line per recorded run: type, preset, elapsed
// against budget, and how it ended.
func renderRunResults(w io.Writer, results []proof.RunResult) {
	if len(results) == 0 {
		return
	}
	sayln(w, "  run results")
	for i := range results {
		r := &results[i]
		say(w, "    %s %s %s/%s %s\n", r.CheckType, r.Preset,
			(time.Duration(r.ElapsedMS) * time.Millisecond).String(), budgetLabel(r.Budget()), runOutcome(*r))
	}
}

// renderActions prints one line per on-complete action: status, attempts,
// and the command.
func renderActions(w io.Writer, actions []proof.ActionResult) {
	if len(actions) == 0 {
		return
	}
	sayln(w, "  on-complete actions")
	for _, a := range actions {
		mode := "waited"
		if a.Spec.Detach {
			mode = "detached"
		}
		say(w, "    %s %s (%s, %d attempt(s)): %s\n", a.Status, a.Spec.Preset, mode, a.Attempts, a.Spec.Subject)
	}
}

func runOutcome(r proof.RunResult) string {
	switch {
	case r.TimedOut:
		return "timed_out"
	case r.Passed:
		return "passed"
	default:
		return "failed"
	}
}

func renderComments(w io.Writer, v app.TaskView) {
	var logs []execution.RecordedLog
	if v.History != nil && len(v.History.Logs) > 0 {
		logs = v.History.Logs
	} else if v.Handoff != nil {
		logs = v.Handoff.RecentLogs
	}
	if len(logs) == 0 {
		return
	}
	sayln(w, "\nCOMMENTS")
	for _, l := range logs {
		say(w, "  %s\n", l.At.Format("2006-01-02 15:04"))
		for _, kv := range []struct{ k, v string }{{"done", l.Done}, {"next", l.Next}, {"learned", l.Learned}, {"note", l.Note}} {
			if kv.v != "" {
				say(w, "      %s: %s\n", kv.k, kv.v)
			}
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

func renderHandoff(w io.Writer, h execution.Handoff) {
	if h.Empty() {
		return
	}
	sayln(w, "HANDOFF")
	if h.LatestNext != "" {
		say(w, "Next:     %s\n", h.LatestNext)
	}
	for _, l := range h.Learnings {
		say(w, "Learned:  %s\n", l)
	}
	for _, l := range h.Inherited {
		ref := l.TaskID
		if l.Key != "" {
			ref = l.Key
		}
		say(w, "From %s: %s\n", ref, l.Learned)
	}
	if f := h.LastFailure; f != nil {
		say(w, "Last failure (%s run #%d): %s\n", f.Mode, f.RunID, f.Summary)
		for _, c := range f.Checks {
			say(w, "  ✗ %s (%s)", c.CheckID, c.Outcome)
			if c.Message != "" {
				say(w, ": %s", c.Message)
			}
			sayln(w)
			for _, tail := range []string{c.StderrTail, c.StdoutTail} {
				for line := range strings.SplitSeq(strings.TrimRight(tail, "\n"), "\n") {
					if line != "" {
						say(w, "      %s\n", line)
					}
				}
			}
			if c.Log != "" {
				say(w, "      full output: %s\n", c.Log)
			}
		}
	}
	for _, wn := range h.Warnings {
		say(w, "Warning:  %s\n", wn)
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
			say(w, "Warning:  %s: %s\n", k, msg)
		}
	}
}

func renderEvidence(w io.Writer, evidence []verification.Evidence, full bool) {
	// A run that verified the submitted revision and then the merge with
	// the target lists each check twice: label the rows with their revision.
	revs := map[string]bool{}
	for i := range evidence {
		ev := &evidence[i]
		revs[ev.Revision] = true
	}
	for i := range evidence {
		ev := &evidence[i]
		kind := "required"
		if !ev.Required {
			kind = "optional"
		}
		at := ""
		if len(revs) > 1 && ev.Revision != "" {
			at = " @" + ev.Revision[:min(12, len(ev.Revision))]
		}
		say(w, "  %-20s %-8s %s exit=%d %s%s\n", ev.CheckID, ev.Outcome, kind, ev.ExitCode, ev.FinishedAt.Sub(ev.StartedAt).Round(time.Millisecond), at)
		if ev.Message != "" {
			say(w, "      %s\n", ev.Message)
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
				say(w, "      --- %s ---\n", name)
				for l := range strings.SplitSeq(out, "\n") {
					say(w, "      %s\n", l)
				}
			}
			if ev.Log != "" {
				say(w, "      full output: %s\n", ev.Log)
			}
		}
	}
}

func renderVerify(w io.Writer, res app.VerifyResult) {
	outcome := "FAILED"
	if res.Passed {
		outcome = "passed"
	}
	say(w, "✓ Verify %s · %s: %s\n", res.Mode, res.TaskID, outcome)
	if res.Revision != "" {
		say(w, "Revision:  %s\n", res.Revision)
	}
	if res.Summary != "" {
		say(w, "Summary:   %s\n", res.Summary)
	}
	switch {
	case res.Completed:
		sayln(w, "Task:      COMPLETE (claim ended)")
	default:
		say(w, "Task:      NOT complete (%s)\n", strings.ToLower(stateLabel(res.Status)))
	}
	if res.Message != "" {
		sayln(w, res.Message)
	}
	for _, warn := range res.Warnings {
		say(w, "Warning:   %s\n", warn)
	}
	if res.ChecksChangedAfterClaim {
		say(w, "Checks:    %d changed since the claim began (see atask show)\n", len(res.CheckChanges))
	}
	renderActions(w, res.Actions)
	renderEvidence(w, res.Evidence, false)
	if res.LogDir != "" {
		say(w, "Logs:      %s (result.json and one log per check)\n", res.LogDir)
	}
}

func renderSummary(w io.Writer, s app.Summary) {
	if s.ProjectID == "" {
		say(w, "Board · %d tasks, %d open, %d claimable, %d active\n", s.Total, s.Open, s.Claimable, s.Active)
	} else {
		say(w, "Project %s · plan rev %d · %d tasks, %d open, %d claimable, %d active\n", s.ProjectID, s.PlanRev, s.Total, s.Open, s.Claimable, s.Active)
	}
	var keys []string
	for st := range s.Counts {
		keys = append(keys, string(st))
	}
	sort.Strings(keys)
	for _, k := range keys {
		say(w, "  %-22s %d\n", k, s.Counts[task.Status(k)])
	}
	switch {
	case s.Done:
		sayln(w, "● done: every task is complete")
	case s.Stalled:
		sayln(w, "○ stalled: nothing can proceed without intervention")
		for _, r := range s.Reasons {
			say(w, "  - %s\n", r)
		}
	}
}

// writeProofLine prints one proof in the grammar it was written in.
func writeProofLine(w io.Writer, label string, spec *proof.ProofSpec) {
	if spec == nil {
		return
	}
	say(w, "  • %-10s %s\n", label+":", proofText(*spec))
}

// proofText renders a ProofSpec as "PRESET: SUBJECT key=value ...".
func proofText(spec proof.ProofSpec) string {
	text := spec.String()
	keys := make([]string, 0, len(spec.Options))
	for k := range spec.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var textSb655 strings.Builder
	for _, k := range keys {
		textSb655.WriteString(" " + k + "=" + spec.Options[k])
	}
	text += textSb655.String()
	return text
}

// budgetLabel shows a run's allowance; a run with none is "unlimited".
func budgetLabel(d time.Duration) string {
	if d <= 0 {
		return "unlimited"
	}
	return d.String()
}

// renderCheckHistory prints one line per recorded check change.
func renderCheckHistory(w io.Writer, changes []task.CheckChange) {
	if len(changes) == 0 {
		return
	}
	sayln(w, "\nCHECK HISTORY")
	for _, c := range changes {
		say(w, "  %s %s by %s: %s → %s\n", c.ChangedAt.Format("2006-01-02 15:04"), c.Field, c.Actor, orDash(c.OldSpec), orDash(c.NewSpec))
	}
}
