package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/execution"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/task"
)

func init() {
	register("take", "atomically take a task: tasks take [TASK] [--lease 1h]", runTake)
	register("log", "record progress: tasks log <token> --done .. --next .. --learned .. --note ..", runLog)
	register("renew-task-lease", "extend a session lease: tasks renew-task-lease <token> [--lease 1h]", runRenew)
	register("renew", "alias of renew-task-lease", runRenew)
	register("finish", "submit the implementation and run its checks: tasks finish <token> [--no-verify]", runFinish)
	register("whoami", "show the task a session token belongs to: tasks whoami <token>", runWhoami)
}

// token resolves the session token from, in order: a positional argument,
// "-" meaning stdin, or the TASKS_SESSION environment variable. Positional
// secrets can appear in process listings; prefer the environment variable
// or stdin in shared environments (see docs/agent-contract.md).
func (c *ctxt) token(positional []string) (execution.Token, error) {
	raw := ""
	switch {
	case len(positional) > 1:
		return "", usage("expected at most one token argument")
	case len(positional) == 1 && positional[0] == "-":
		line, err := bufio.NewReader(c.env.Stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fault.Wrap(err, fault.CodeInvalidSession, "read token from stdin")
		}
		raw = line
	case len(positional) == 1:
		raw = positional[0]
	default:
		raw = c.env.Getenv("TASKS_SESSION")
	}
	if strings.TrimSpace(raw) == "" {
		return "", usage("session token required: pass it as an argument, as '-' to read stdin, or set TASKS_SESSION")
	}
	return execution.ParseToken(raw)
}

func runTake(ctx context.Context, c *ctxt, args []string) error {
	lease := c.fs.Duration("lease", 0, "lease duration (default 1h, min 1s, max 24h)")
	if err := c.parse(args); err != nil {
		return err
	}
	if len(c.args) > 1 {
		return usage("take accepts at most one task id")
	}
	req := app.TakeRequest{Lease: *lease}
	if len(c.args) == 1 {
		id, err := task.ParseID(c.args[0])
		if err != nil {
			return err
		}
		req.Task = &id
	} else {
		pid, err := c.resolveProject(ctx)
		if err != nil {
			return err
		}
		req.Project = pid
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	s, err := e.Take(ctx, req)
	if err != nil {
		return err
	}
	return c.emit(s, func(w io.Writer) {
		verb := "took"
		if s.Resumed {
			verb = "resumed"
		}
		fmt.Fprintf(w, "%s %s (attempt %d), lease until %s\n", verb, s.TaskID, s.AttemptSeq, s.ExpiresAt.Format(time.RFC3339))
		fmt.Fprintf(w, "session token: %s\n", s.Token)
		fmt.Fprintln(w)
		renderTaskView(w, s.Task)
		renderHandoff(w, s.Handoff)
	})
}

func runLog(ctx context.Context, c *ctxt, args []string) error {
	entry := execution.LogEntry{}
	c.fs.StringVar(&entry.Done, "done", "", "progress made")
	c.fs.StringVar(&entry.Next, "next", "", "remaining work / next action")
	c.fs.StringVar(&entry.Learned, "learned", "", "reusable discovery")
	c.fs.StringVar(&entry.Note, "note", "", "warning, question, or context")
	if err := c.parse(args); err != nil {
		return err
	}
	tok, err := c.token(c.args)
	if err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	rec, err := e.Log(ctx, tok, entry)
	if err != nil {
		return err
	}
	return c.emit(rec, func(w io.Writer) { fmt.Fprintf(w, "logged entry #%d\n", rec.Seq) })
}

func runRenew(ctx context.Context, c *ctxt, args []string) error {
	lease := c.fs.Duration("lease", 0, "new lease duration from now (default 1h)")
	if err := c.parse(args); err != nil {
		return err
	}
	tok, err := c.token(c.args)
	if err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	exp, err := e.Renew(ctx, tok, *lease)
	if err != nil {
		return err
	}
	type out struct {
		LeaseExpiresAt time.Time `json:"lease_expires_at"`
	}
	return c.emit(out{exp}, func(w io.Writer) { fmt.Fprintf(w, "lease renewed until %s\n", exp.Format(time.RFC3339)) })
}

func runFinish(ctx context.Context, c *ctxt, args []string) error {
	noVerify := c.fs.Bool("no-verify", false, "record the submission without running checks (run `tasks verify` later)")
	if err := c.parse(args); err != nil {
		return err
	}
	tok, err := c.token(c.args)
	if err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	res, err := e.Finish(ctx, tok, app.FinishOptions{NoVerify: *noVerify})
	if err != nil {
		return err
	}
	return c.emit(res, func(w io.Writer) { renderSubmission(w, res) })
}

func renderSubmission(w io.Writer, res app.SubmissionResult) {
	fmt.Fprintf(w, "%s: submission #%d (attempt %d) verification %s, status %s\n", res.TaskID, res.SubmissionID, res.AttemptSeq, res.Verification, res.Status)
	if res.Revision != "" {
		fmt.Fprintf(w, "revision: %s\n", res.Revision)
	}
	if res.Summary != "" {
		fmt.Fprintf(w, "run %d: %s\n", res.RunID, res.Summary)
	}
	if res.Message != "" {
		fmt.Fprintln(w, res.Message)
	}
}

func runWhoami(ctx context.Context, c *ctxt, args []string) error {
	if err := c.parse(args); err != nil {
		return err
	}
	tok, err := c.token(c.args)
	if err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	v, err := e.Whoami(ctx, tok)
	if err != nil {
		return err
	}
	return c.emit(v, func(w io.Writer) { renderTaskView(w, v) })
}
