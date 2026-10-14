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
	"github.com/zachbornheimer/ai-task/internal/verification"
	"github.com/zachbornheimer/ai-task/internal/workspace"
)

func init() {
	register("claim", "claim work: atask claim [REF] [--wait] | atask claim renew [token|-] | atask claim release [token|-] [--note ..] [--failed]", runClaim)
	register("log", "record progress under the current session: atask log --done .. --next .. --learned .. --note ..", runLog)
	register("verify", "run checks under the current session: atask verify proof|regression|complete | atask verify groups [REF]", runVerify)
}

// token resolves a REQUIRED session token: a positional argument, "-" for
// one line of stdin, --session, ATASK_SESSION, or the token the claim stored
// in the task worktree the command runs in (so an agent started there
// needs no token at all). A stale process sits in a quarantined worktree
// whose stored token is its own, expired, one. Human read commands never
// print tokens.
//
// When the working directory is a task worktree the token must belong to
// that task: a token for another task is refused before any write, so a
// mis-exported ATASK_SESSION cannot act on the wrong task.
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
		raw = c.env.Getenv("ATASK_SESSION")
	}
	if strings.TrimSpace(raw) == "" {
		raw = workspace.LoadToken(ctx, c.env.Cwd)
	}
	if strings.TrimSpace(raw) == "" {
		return "", usage("session token required: run inside the task worktree, pass it as an argument, '-' to read stdin, --session, or set ATASK_SESSION")
	}
	tok, err := execution.ParseToken(raw)
	if err != nil {
		return "", err
	}
	return tok, c.guardWorkspace(ctx, tok)
}

// guardWorkspace refuses a token that does not own the task whose worktree
// is the working directory (branch atask/<task-id>). Outside a task worktree
// it does nothing; a token that resolves to nothing is left for the
// command itself to report.
func (c *ctxt) guardWorkspace(ctx context.Context, tok execution.Token) error {
	branch := workspace.CurrentBranch(ctx, c.env.Cwd)
	here := strings.TrimPrefix(branch, "atask/")
	if here == branch || !task.IsID(here) {
		return nil
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	v, err := e.Whoami(ctx, tok)
	if err != nil {
		return nil
	}
	if string(v.ID) != here {
		return fault.New(fault.CodeInvalidSession, "session token belongs to %s but the working directory is the worktree of %s; run in %s's worktree or use its token", v.ID, here, v.ID)
	}
	return nil
}

// claimSubcommand finds "renew" or "release" among args, however flags
// are interleaved (`atask claim --json renew` dispatches like `atask claim
// renew --json`), and returns the remaining args.
func claimSubcommand(args []string) (string, []string) {
	valueFlags := map[string]bool{"--lease": true, "--session": true, "--note": true, "--db": true, "--project": true, "--output": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "renew" || a == "release" {
			return a, append(append([]string{}, args[:i]...), args[i+1:]...)
		}
		if a == "--" {
			return "", args
		}
		if strings.HasPrefix(a, "-") {
			if valueFlags[strings.TrimPrefix(a, "-")] || valueFlags[a] {
				if !strings.Contains(a, "=") {
					i++ // skip the flag's value
				}
			}
			continue
		}
		return "", args // the first positional is a task reference
	}
	return "", args
}

func runClaim(ctx context.Context, c *ctxt, args []string) error {
	// Subcommands renew/release take the token explicitly.
	if sub, rest := claimSubcommand(args); sub != "" {
		return c.claimSub(ctx, sub, rest)
	}
	wait := c.fs.Bool("wait", false, "block until work is claimable, the project is done, or it is stalled")
	lease := c.fs.Duration("lease", 0, "lease duration (default 30m, min 5m, max 4h; every authenticated command renews it)")
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
	return c.emit(s, func(w io.Writer) { renderSession(w, s) })
}

// renderSession is the human acknowledgement of a claim.
func renderSession(w io.Writer, s app.Session) {
	verb := "Claimed"
	if s.Resumed {
		verb = "Resumed"
	}
	fmt.Fprintf(w, "✓ %s %s · %s (attempt %d)\n", verb, s.Task.ID, s.Task.Title, s.AttemptSeq)
	fmt.Fprintf(w, "Lease:     until %s (renew with `atask claim renew`)\n", s.LeaseUntil.Format(time.RFC3339))
	if s.Workspace != "" {
		fmt.Fprintf(w, "Workspace: %s", s.Workspace)
		if s.Branch != "" {
			fmt.Fprintf(w, " (branch %s)", s.Branch)
		}
		fmt.Fprintln(w)
	}
	if s.TargetBranch != "" {
		fmt.Fprintf(w, "Target:    %s", s.TargetBranch)
		if s.TargetRevision != "" {
			fmt.Fprintf(w, " at %s", s.TargetRevision[:min(12, len(s.TargetRevision))])
		}
		fmt.Fprintln(w, " (verified work is promoted there; on BASE_CONFLICT merge it into your branch)")
	}
	if len(s.Regression) > 0 {
		fmt.Fprintf(w, "Regression: %d check(s) run by `verify complete` after the task checks:\n", len(s.Regression))
		for _, ch := range s.Regression {
			fmt.Fprintf(w, "  - %s: %s\n", ch.ID, app.CheckText(ch))
		}
	}
	if s.WorkspaceDirty {
		fmt.Fprintln(w, "Warning:   workspace has uncommitted changes from a previous attempt; review `git status` before editing")
	}
	if s.QuarantinedWorkspace != "" {
		fmt.Fprintf(w, "Previous:  attempt did not end cleanly; its worktree was quarantined at %s\n", s.QuarantinedWorkspace)
	}
	fmt.Fprintf(w, "Token:     %s (also stored in the worktree; `atask` commands run there need no token)\n", s.Token)
	fmt.Fprintln(w)
	renderShow(w, s.Task, false, painter{})
	renderHandoff(w, s.Handoff)
}

func (c *ctxt) claimSub(ctx context.Context, sub string, args []string) error {
	note := c.fs.String("note", "", "why the task is being released (logged)")
	failed := c.fs.Bool("failed", false, "record a failed attempt (retry bookkeeping)")
	session := c.fs.String("session", "", "session token (default: ATASK_SESSION or the worktree's stored token)")
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
		v, err := e.ReleaseTask(ctx, app.ReleaseRequest{Token: tok, Note: *note, Failed: *failed})
		if err != nil {
			return err
		}
		return c.emit(v, func(w io.Writer) {
			fmt.Fprintf(w, "✓ Released %s · %s\nState:    %s %s\n", v.ID, v.Title, v.Status.Glyph(), stateLabel(v.Status))
			if v.Note != "" {
				fmt.Fprintf(w, "Note:     %s\n", v.Note)
			}
		})
	}
}

func runLog(ctx context.Context, c *ctxt, args []string) error {
	entry := execution.LogEntry{}
	c.fs.StringVar(&entry.Done, "done", "", "progress made")
	c.fs.StringVar(&entry.Next, "next", "", "remaining work / next action")
	c.fs.StringVar(&entry.Learned, "learned", "", "reusable discovery")
	c.fs.StringVar(&entry.Note, "note", "", "warning, question, or context")
	session := c.fs.String("session", "", "session token (default: ATASK_SESSION or the worktree's stored token)")
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
	session := c.fs.String("session", "", "session token (default: ATASK_SESSION or the worktree's stored token)")
	reverify := c.fs.Bool("reverify", false, "complete only: run proof and regression again against the revision that completed the task; the task stays complete")
	if err := c.parse(args); err != nil {
		return err
	}
	if len(c.args) < 1 {
		return usage("verify needs a mode: `atask verify proof`, `atask verify regression`, `atask verify complete`, or `atask verify complete GROUP`")
	}
	if c.args[0] == string(verification.ModeComplete) && len(c.args) == 2 && *session == "" {
		if handled, err := c.verifyGroup(ctx, c.args[1]); handled {
			return err
		}
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
	var res app.VerifyResult
	switch {
	case *reverify && mode != verification.ModeComplete:
		return usage("--reverify applies to `verify complete` only")
	case *reverify:
		res, err = e.Reverify(ctx, tok)
	default:
		res, err = e.Verify(ctx, tok, mode)
	}
	if err != nil {
		return err
	}
	return c.emit(res, func(w io.Writer) { renderVerify(w, res) })
}

// verifyGroup completes the group ref names: its proofs run on the merge of
// every child's completed revision. handled is false when ref is not a
// group, so the caller treats it as a session token.
func (c *ctxt) verifyGroup(ctx context.Context, ref string) (handled bool, err error) {
	pid, err := c.resolveProject(ctx)
	if err != nil {
		return false, nil
	}
	e, err := c.engine(ctx)
	if err != nil {
		return false, nil
	}
	v, err := e.Show(ctx, pid, ref, false)
	if err != nil || v.Kind != task.KindGroup {
		return false, nil
	}
	res, err := e.VerifyGroup(ctx, pid, ref)
	if err != nil {
		return true, err
	}
	return true, c.emit(res, func(w io.Writer) { renderVerify(w, res) })
}

func runContext(ctx context.Context, c *ctxt, args []string) error {
	if err := c.parse(args); err != nil {
		return err
	}
	if len(c.args) == 0 {
		return c.emit(map[string]string{"prompt": AgentContract}, func(w io.Writer) {
			_, _ = io.WriteString(w, AgentContract)
		})
	}
	if len(c.args) != 1 {
		return usage("context takes no arguments, or one task id")
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	ref := c.args[0]
	var cx app.Context
	if task.IsID(ref) {
		cx, err = e.Context(ctx, "", ref, "", false)
	} else {
		id, rerr := c.resolveProject(ctx)
		if rerr != nil {
			return rerr
		}
		cx, err = e.Context(ctx, id, ref, "", false)
	}
	if err != nil {
		return err
	}
	return c.emit(cx, func(w io.Writer) {
		_, _ = io.WriteString(w, cx.Prompt)
	})
}

func init() {
	register("context", "operating contract, or worker handoff for a task id", runContext)
}
