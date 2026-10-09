package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

func init() {
	register("verify", "run the checks for a task's newest submission: tasks verify <task> [--retry] [--again] [--no-reuse]", runVerify)
	register("evidence", "show verification runs and evidence: tasks evidence <task> [--run N] [--full]", runEvidence)
	register("policy", "show or replace a task's verification policy: tasks policy <task> [--check ..] [--optional-check ..] [--policy-json ..] [--clear]", runPolicy)
}

func runVerify(ctx context.Context, c *ctxt, args []string) error {
	retry := c.fs.Bool("retry", false, "restart a run stuck in 'running' (its process is gone)")
	again := c.fs.Bool("again", false, "re-verify a submission that already passed, under the current policy")
	noReuse := c.fs.Bool("no-reuse", false, "execute every check even if identical evidence exists at this revision")
	if err := c.parse(args); err != nil {
		return err
	}
	if len(c.args) != 1 {
		return usage("verify needs exactly one task id")
	}
	id, err := task.ParseID(c.args[0])
	if err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	res, err := e.Verify(ctx, id, app.VerifyOptions{Retry: *retry, Again: *again, NoReuse: *noReuse})
	if err != nil {
		return err
	}
	return c.emit(res, func(w io.Writer) {
		fmt.Fprintf(w, "%s: run %d %s (%s), status %s\n", res.TaskID, res.RunID, res.Verification, res.Summary, res.Status)
		renderEvidence(w, res.Evidence, false)
	})
}

func renderEvidence(w io.Writer, evidence []verification.Evidence, full bool) {
	for _, ev := range evidence {
		kind := "required"
		if !ev.Required {
			kind = "optional"
		}
		extra := ""
		if ev.Reused {
			extra = " (reused)"
		}
		fmt.Fprintf(w, "  %-20s %-8s %s exit=%d %s%s\n", ev.CheckID, ev.Outcome, kind, ev.ExitCode, ev.FinishedAt.Sub(ev.StartedAt).Round(time.Millisecond), extra)
		if ev.Message != "" {
			fmt.Fprintf(w, "      %s\n", ev.Message)
		}
		if ev.Outcome != verification.OutcomePassed || full {
			for name, out := range map[string]string{"stdout": ev.Stdout, "stderr": ev.Stderr} {
				out = strings.TrimSpace(out)
				if out == "" {
					continue
				}
				if !full && len(out) > 2000 {
					out = "... " + out[len(out)-2000:]
				}
				fmt.Fprintf(w, "      --- %s ---\n", name)
				for _, line := range strings.Split(out, "\n") {
					fmt.Fprintf(w, "      %s\n", line)
				}
			}
		}
	}
}

func runEvidence(ctx context.Context, c *ctxt, args []string) error {
	run := c.fs.Int64("run", 0, "show only this run")
	full := c.fs.Bool("full", false, "include full captured output for every check")
	if err := c.parse(args); err != nil {
		return err
	}
	if len(c.args) != 1 {
		return usage("evidence needs exactly one task id")
	}
	id, err := task.ParseID(c.args[0])
	if err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	page, err := e.Evidence(ctx, id, *run)
	if err != nil {
		return err
	}
	return c.emit(page, func(w io.Writer) {
		for _, s := range page.Submissions {
			fmt.Fprintf(w, "submission #%d attempt %d revision %s at %s: %s\n", s.ID, s.AttemptSeq, orDash(s.Revision), s.SubmittedAt.Format(time.RFC3339), s.Verification)
		}
		for _, r := range page.Runs {
			fmt.Fprintf(w, "run %d (submission %d) %s policy %s\n", r.ID, r.SubmissionID, r.Status, shortDigest(r.PolicyDigest))
			if r.Summary != "" {
				fmt.Fprintf(w, "  %s\n", r.Summary)
			}
			renderEvidence(w, r.Evidence, *full)
		}
	})
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func shortDigest(d string) string {
	if len(d) > 19 {
		return d[:19]
	}
	return d
}

func runPolicy(ctx context.Context, c *ctxt, args []string) error {
	var checks, optionalChecks stringList
	c.fs.Var(&checks, "check", "required task check: \"[id:] command args...\" (repeatable)")
	c.fs.Var(&optionalChecks, "optional-check", "optional task check (repeatable)")
	policyJSON := c.fs.String("policy-json", "", "full verification policy as JSON")
	clear := c.fs.Bool("clear", false, "remove every task-level check")
	if err := c.parse(args); err != nil {
		return err
	}
	if len(c.args) != 1 {
		return usage("policy needs exactly one task id")
	}
	id, err := task.ParseID(c.args[0])
	if err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	changing := *clear || *policyJSON != "" || len(checks)+len(optionalChecks) > 0
	if !changing {
		v, err := e.Show(ctx, id)
		if err != nil {
			return err
		}
		type out struct {
			TaskID task.ID             `json:"task_id"`
			Policy verification.Policy `json:"policy"`
			Digest string              `json:"policy_digest"`
		}
		return c.emit(out{id, v.Task.Verification, v.PolicyDigest}, func(w io.Writer) { renderPolicy(w, v.Task.Verification, v.PolicyDigest) })
	}
	var policy verification.Policy
	switch {
	case *clear:
	case *policyJSON != "":
		if err := json.Unmarshal([]byte(*policyJSON), &policy); err != nil {
			return usage("--policy-json: %v", err)
		}
	default:
		for i, raw := range checks {
			cs, err := parseCheck(raw, i+1, true)
			if err != nil {
				return err
			}
			policy.TaskChecks = append(policy.TaskChecks, cs)
		}
		for i, raw := range optionalChecks {
			cs, err := parseCheck(raw, len(checks)+i+1, false)
			if err != nil {
				return err
			}
			policy.TaskChecks = append(policy.TaskChecks, cs)
		}
	}
	v, err := e.SetTaskPolicy(ctx, id, policy)
	if err != nil {
		return err
	}
	return c.emit(v, func(w io.Writer) {
		renderPolicy(w, v.Task.Verification, v.PolicyDigest)
		fmt.Fprintf(w, "status: %s\n", v.Status)
	})
}

func renderPolicy(w io.Writer, p verification.Policy, digest string) {
	fmt.Fprintf(w, "policy %s\n", digest)
	for _, group := range []struct {
		name   string
		checks []verification.CheckSpec
	}{{"task check", p.TaskChecks}, {"regression", p.Regression}} {
		for _, ch := range group.checks {
			kind := "required"
			if !ch.Required {
				kind = "optional"
			}
			fmt.Fprintf(w, "  %s %s (%s, timeout %s): %s\n", group.name, ch.ID, kind, ch.EffectiveTimeout(), strings.Join(ch.Command, " "))
		}
	}
}
