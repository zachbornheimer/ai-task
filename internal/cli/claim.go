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
// one line of stdin, --session, AT_SESSION, or the token the claim stored
// in the task worktree the command runs in (so an agent started there
// needs no token at all). A stale process sits in a quarantined worktree
// whose stored token is its own, expired, one. Human read commands never
// print tokens.
//
// When the working directory is a task worktree the token must belong to
// that task: a token for another task is refused before any write, so a
// mis-exported AT_SESSION cannot act on the wrong task.
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
	tok, err := execution.ParseToken(raw)
	if err != nil {
		return "", err
	}
	return tok, c.guardWorkspace(ctx, tok)
}

// guardWorkspace refuses a token that does not own the task whose worktree
// is the working directory (branch at/<task-id>). Outside a task worktree
// it does nothing; a token that resolves to nothing is left for the
// command itself to report.
func (c *ctxt) guardWorkspace(ctx context.Context, tok execution.Token) error {
	branch := workspace.CurrentBranch(ctx, c.env.Cwd)
	here := strings.TrimPrefix(branch, "at/")
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
// are interleaved (`at claim --json renew` dispatches like `at claim
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
	fmt.Fprintf(w, "Lease:     until %s (renew with `at claim renew`)\n", s.LeaseUntil.Format(time.RFC3339))
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
		fmt.Fprintln(w, " (verified work is promoted there; on INTEGRATION_FAILED merge it into your branch)")
	}
	if len(s.Regression) > 0 {
		fmt.Fprintf(w, "Regression: %d check(s) run by `verify complete` after the task checks:\n", len(s.Regression))
		for _, ch := range s.Regression {
			fmt.Fprintf(w, "  - %s: %s\n", ch.ID, strings.Join(ch.Command, " "))
		}
	}
	if s.WorkspaceDirty {
		fmt.Fprintln(w, "Warning:   workspace has uncommitted changes from a previous attempt; review `git status` before editing")
	}
	if s.QuarantinedWorkspace != "" {
		fmt.Fprintf(w, "Previous:  attempt did not end cleanly; its worktree was quarantined at %s\n", s.QuarantinedWorkspace)
	}
	fmt.Fprintf(w, "Token:     %s (also stored in the worktree; `at` commands run there need no token)\n", s.Token)
	fmt.Fprintln(w)
	renderShow(w, s.Task, false)
	renderHandoff(w, s.Handoff)
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
		return usage("verify needs a mode: `at verify task`, `at verify regression`, `at verify complete`, or `at verify groups [REF]`")
	}
	if c.args[0] == "groups" || c.args[0] == "group" {
		return c.verifyGroups(ctx, c.args[1:])
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

// verifyGroups runs the epic checks of every group whose members are
// complete (or of one named group, even after a failure on the same
// revision), the way a waiting claim would, and reports each group's
// verification state.
func (c *ctxt) verifyGroups(ctx context.Context, args []string) error {
	if len(args) > 1 {
		return usage("verify groups takes at most one group reference")
	}
	pid, err := c.resolveProject(ctx)
	if err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	var only task.ID
	if len(args) == 1 {
		v, err := e.Show(ctx, pid, args[0], false)
		if err != nil {
			return err
		}
		if v.Kind != task.KindGroup {
			return usage("%s is not a group", args[0])
		}
		only = v.ID
	}
	ran, runErr := e.RunPendingGroups(ctx, pid, only)
	snap, err := e.List(ctx, app.ListQuery{ProjectID: pid, Filter: app.FilterAll})
	if err != nil {
		return err
	}
	type out struct {
		Ran    bool            `json:"ran"`
		Groups []app.GroupView `json:"groups"`
	}
	o := out{Ran: ran}
	for _, g := range snap.Groups {
		if len(g.Checks) > 0 && (only == "" || g.ID == only) {
			o.Groups = append(o.Groups, g)
		}
	}
	if runErr != nil && fault.CodeOf(runErr) == fault.CodeInternal {
		return runErr
	}
	if err := c.emit(o, func(w io.Writer) {
		if !ran {
			fmt.Fprintln(w, "No epic was ready to verify.")
		}
		for _, g := range o.Groups {
			fmt.Fprintln(w, groupLine(g))
			if g.Verification != nil && g.Verification.Summary != "" {
				fmt.Fprintf(w, "    %s\n", g.Verification.Summary)
			}
		}
	}); err != nil {
		return err
	}
	return runErr
}

func runContext(ctx context.Context, c *ctxt, args []string) error {
	session := c.fs.String("session", "", "session token (default: AT_SESSION or the worktree's stored token)")
	withRules := c.fs.Bool("with-rules", false, "append the standing rules (AGENTS.md) for an agent that does not read that file")
	if err := c.parse(args); err != nil {
		return err
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	var (
		ref string
		tok execution.Token
	)
	switch {
	case len(c.args) > 1:
		return usage("context takes at most one task reference")
	case len(c.args) == 1 && c.args[0] != "-" && !strings.HasPrefix(c.args[0], "sess-"):
		ref = c.args[0]
	case len(c.args) == 0 && *session == "" && c.env.Getenv("AT_SESSION") == "" && workspace.LoadToken(ctx, c.env.Cwd) == "":
		// No task in hand anywhere: the project's context.
	default:
		// The session's own task: the worktree's stored token, AT_SESSION,
		// a positional token, '-' for stdin, or --session.
		if tok, err = c.token(ctx, c.args, *session); err != nil {
			return err
		}
	}
	pid := project.ID("")
	if (ref != "" && !task.IsID(ref)) || (ref == "" && tok == "") {
		if pid, err = c.resolveProject(ctx); err != nil {
			return err
		}
	}
	cx, err := e.Context(ctx, pid, ref, tok, *withRules)
	if err != nil {
		return err
	}
	return c.emit(cx, func(w io.Writer) { io.WriteString(w, cx.Prompt) })
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

func init() {
	register("context", "working context for a coding agent: at context [REF] [--with-rules] (no REF: this session's task, or the plan when none is in hand)", runContext)
}
