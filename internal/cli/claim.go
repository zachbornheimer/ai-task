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
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/task"
	"github.com/zachbornheimer/ai-task/internal/verification"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

func init() {
	register("claim", "claim work: at claim [REF] [--wait] | at claim renew [token|-] | at claim release [token|-] [--note ..] [--failed]", runClaim)
	register("log", "record progress under the current session: at log --done .. --next .. --learned .. --note ..", runLog)
	register("verify", "run checks under the current session: at verify task|regression|complete", runVerify)
	register("whoami", "show the task of the current session token", runWhoami)
}

// token resolves a REQUIRED session token: a positional argument, "-" for
// one line of stdin, --session, AT_SESSION, or the token the engine stored
// in the task worktree when the task was claimed (so commands run inside
// the worktree need no token at all). Human read commands never print
// tokens.
func (c *ctxt) token(ctx context.Context, positional []string, flag string) (execution.Token, error) {
	raw := flag
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
	}
	if strings.TrimSpace(raw) == "" {
		raw = c.env.Getenv("AT_SESSION")
	}
	if strings.TrimSpace(raw) == "" {
		raw = workspace.LoadToken(ctx, c.env.Cwd)
	}
	if strings.TrimSpace(raw) == "" {
		return "", usage("session token required: run inside the task worktree, pass it as an argument, '-' to read stdin, --session, or set AT_SESSION")
	}
	return execution.ParseToken(raw)
}

func runClaim(ctx context.Context, c *ctxt, args []string) error {
	// Subcommands renew/release take the token explicitly.
	if len(args) > 0 && (args[0] == "renew" || args[0] == "release") {
		return c.claimSub(ctx, args[0], args[1:])
	}
	wait := c.fs.Bool("wait", false, "block until work is claimable, the project is done, or it is stalled")
	lease := c.fs.Duration("lease", 0, "lease duration (default 30m, min 5m, max 4h; renewed by every authenticated command and the post-commit hook)")
	if err := c.parse(args); err != nil {
		return err
	}
	if len(c.args) > 1 {
		return usage("claim accepts at most one task reference")
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	req := app.ClaimRequest{Lease: *lease, Wait: *wait}
	pid, perr := c.resolveProject(ctx)
	if len(c.args) == 1 {
		ref := c.args[0]
		if task.IsID(ref) {
			id := task.ID(ref)
			req.TaskID = &id
		} else {
			if perr != nil {
				return perr
			}
			v, err := e.Show(ctx, pid, ref, false)
			if err != nil {
				return err
			}
			req.TaskID = &v.ID
		}
		if perr == nil {
			req.ProjectID = pid
		}
	} else {
		if perr != nil {
			return perr
		}
		req.ProjectID = pid
	}
	s, err := e.Claim(ctx, req)
	if err != nil {
		return err
	}
	return c.emit(s, func(w io.Writer) {
		verb := "Claimed"
		if s.Resumed {
			verb = "Resumed"
		}
		fmt.Fprintf(w, "✓ %s %s · %s (attempt %d)\n", verb, s.Task.ID, s.Task.Title, s.AttemptSeq)
		fmt.Fprintf(w, "Lease:     until %s (renew with `at claim renew`)\n", s.LeaseUntil.Format(time.RFC3339))
		if s.Workspace != "" {
			fmt.Fprintf(w, "Workspace: %s", s.Workspace)
			if s.Branch != "" {
				fmt.Fprintf(w, " (branch %s)", s.Branch)
			}
			fmt.Fprintln(w)
		}
		if s.WorkspaceDirty {
			fmt.Fprintln(w, "Warning:   workspace has uncommitted changes from a previous attempt; review `git status` before editing")
		}
		fmt.Fprintf(w, "Token:     %s (also stored in the worktree; `at` commands run there need no token)\n", s.Token)
		fmt.Fprintln(w)
		renderShow(w, s.Task, false)
		renderHandoff(w, s.Handoff)
	})
}

func (c *ctxt) claimSub(ctx context.Context, sub string, args []string) error {
	note := c.fs.String("note", "", "why the task is being released (logged)")
	failed := c.fs.Bool("failed", false, "record a failed attempt (retry bookkeeping)")
	session := c.fs.String("session", "", "session token (default: AT_SESSION or the worktree's stored token)")
	if err := c.parse(args); err != nil {
		return err
	}
	tok, err := c.token(ctx, c.args, *session)
	if err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	switch sub {
	case "renew":
		exp, err := e.Renew(ctx, tok)
		if err != nil {
			return err
		}
		type out struct {
			LeaseUntil time.Time `json:"lease_until"`
		}
		return c.emit(out{exp}, func(w io.Writer) { fmt.Fprintf(w, "✓ Renewed · lease until %s\n", exp.Format(time.RFC3339)) })
	default:
		v, err := e.Release(ctx, tok, app.ReleaseOptions{Note: *note, Failed: *failed})
		if err != nil {
			return err
		}
		return c.emit(v, func(w io.Writer) {
			fmt.Fprintf(w, "✓ Released %s · %s\nState:    %s %s\n", v.ID, v.Title, v.Status.Glyph(), stateLabel(v.Status))
		})
	}
}

func runLog(ctx context.Context, c *ctxt, args []string) error {
	entry := execution.LogEntry{}
	c.fs.StringVar(&entry.Done, "done", "", "progress made")
	c.fs.StringVar(&entry.Next, "next", "", "remaining work / next action")
	c.fs.StringVar(&entry.Learned, "learned", "", "reusable discovery")
	c.fs.StringVar(&entry.Note, "note", "", "warning, question, or context")
	session := c.fs.String("session", "", "session token (default: AT_SESSION or the worktree's stored token)")
	if err := c.parse(args); err != nil {
		return err
	}
	tok, err := c.token(ctx, c.args, *session)
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
	return c.emit(rec, func(w io.Writer) { fmt.Fprintf(w, "✓ Logged entry #%d\n", rec.Seq) })
}

func runVerify(ctx context.Context, c *ctxt, args []string) error {
	session := c.fs.String("session", "", "session token (default: AT_SESSION or the worktree's stored token)")
	if err := c.parse(args); err != nil {
		return err
	}
	if len(c.args) < 1 {
		return usage("verify needs a mode: `at verify task`, `at verify regression`, or `at verify complete`")
	}
	mode, err := verification.ParseMode(c.args[0])
	if err != nil {
		return usage("%v", err)
	}
	tok, err := c.token(ctx, c.args[1:], *session)
	if err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	res, err := e.Verify(ctx, tok, mode)
	if err != nil {
		return err
	}
	return c.emit(res, func(w io.Writer) { renderVerify(w, res) })
}

func runWhoami(ctx context.Context, c *ctxt, args []string) error {
	session := c.fs.String("session", "", "session token (default: AT_SESSION or the worktree's stored token)")
	if err := c.parse(args); err != nil {
		return err
	}
	tok, err := c.token(ctx, c.args, *session)
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
	return c.emit(v, func(w io.Writer) { renderShow(w, v, false) })
}

var _ = project.ID("")
