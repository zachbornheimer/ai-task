package cli

import (
	"bufio"
	"context"
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
	register("claim", "claim work: atask claim [REF] [--wait] | atask claim renew [token|-] | atask claim release [token|-] [--note ..] [--failed]", runClaim)
	register("log", "record progress under the current session: atask log --done .. --next .. --learned .. --note ..", runLog)
	register("verify", "run checks under the current session: atask verify proof|regression|complete", runVerify)
}

// token resolves a REQUIRED session token: a positional argument, "-" for
// one line of stdin, ATASK_SESSION, or the token the claim stored
// in the task worktree the command runs in (so an agent started there
// needs no token at all). A stale process sits in a quarantined worktree
// whose stored token is its own, expired, one. Human read commands never
// print tokens.
//
// When the working directory is a task worktree the token must belong to
// that task: a token for another task is refused before any write, so a
// mis-exported ATASK_SESSION cannot act on the wrong task.
func (c *ctxt) token(ctx context.Context, positional []string) (execution.Token, error) {
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
		if !looksLikeToken(positional[0]) {
			return "", strayArgument(positional[0])
		}
		raw = positional[0]
	}
	if strings.TrimSpace(raw) == "" {
		raw = c.env.Getenv("ATASK_SESSION")
	}
	if strings.TrimSpace(raw) == "" {
		raw = workspace.LoadToken(ctx, c.env.Cwd)
	}
	if strings.TrimSpace(raw) == "" {
		return "", usage("session token required: run inside the task worktree, pass it as an argument, '-' to read stdin, or set ATASK_SESSION")
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
	// A token that resolves to nothing is left for the command to report.
	if v, err := e.Whoami(ctx, tok); err == nil && string(v.ID) != here {
		return fault.New(fault.CodeInvalidSession, "session token belongs to %s but the working directory is the worktree of %s; run in %s's worktree or use its token", v.ID, here, v.ID)
	}
	return nil
}

// claimSubcommand finds "renew" or "release" among args, however flags
// are interleaved (`atask claim --json renew` dispatches like `atask claim
// renew --json`), and returns the remaining args.
func claimSubcommand(args []string) (string, []string) {
	valueFlags := map[string]bool{"--note": true, "--db": true, "--project": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "renew" || a == "release" {
			return a, append(append([]string{}, args[:i]...), args[i+1:]...)
		}
		if a == "--" {
			return "", args
		}
		if after, ok := strings.CutPrefix(a, "-"); ok {
			if valueFlags[after] || valueFlags[a] {
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
	req := app.ClaimRequest{Wait: *wait}
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
	say(w, "✓ %s %s · %s (attempt %d)\n", verb, s.Task.ID, s.Task.Title, s.AttemptSeq)
	say(w, "Lease:     until %s (renew with `atask claim renew`)\n", s.LeaseUntil.Format(time.RFC3339))
	if s.Workspace != "" {
		say(w, "Workspace: %s", s.Workspace)
		if s.Branch != "" {
			say(w, " (branch %s)", s.Branch)
		}
		sayln(w)
	}
	if s.TargetBranch != "" {
		say(w, "Target:    %s", s.TargetBranch)
		if s.TargetRevision != "" {
			say(w, " at %s", s.TargetRevision[:min(12, len(s.TargetRevision))])
		}
		sayln(w, " (verified work is promoted there; on BASE_CONFLICT merge it into your branch)")
	}
	if len(s.Regression) > 0 {
		say(w, "Regression: %d check(s) run by `verify complete` after the task checks:\n", len(s.Regression))
		for _, ch := range s.Regression {
			say(w, "  - %s: %s\n", ch.ID, app.CheckText(ch))
		}
	}
	if s.WorkspaceDirty {
		sayln(w, "Warning:   workspace has uncommitted changes from a previous attempt; review `git status` before editing")
	}
	if s.QuarantinedWorkspace != "" {
		say(w, "Previous:  attempt did not end cleanly; its worktree was quarantined at %s\n", s.QuarantinedWorkspace)
	}
	say(w, "Token:     %s (also stored in the worktree; `atask` commands run there need no token)\n", s.Token)
	sayln(w)
	renderShow(w, s.Task, false, painter{})
	renderHandoff(w, s.Handoff)
}

func (c *ctxt) claimSub(ctx context.Context, sub string, args []string) error {
	note := c.fs.String("note", "", "why the task is being released (logged)")
	failed := c.fs.Bool("failed", false, "record a failed attempt (retry bookkeeping)")
	if err := c.parse(args); err != nil {
		return err
	}
	tok, err := c.token(ctx, c.args)
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
		return c.emit(out{exp}, func(w io.Writer) { say(w, "✓ Renewed · lease until %s\n", exp.Format(time.RFC3339)) })
	default:
		v, err := e.ReleaseTask(ctx, app.ReleaseRequest{Token: tok, Note: *note, Failed: *failed})
		if err != nil {
			return err
		}
		return c.emit(v, func(w io.Writer) {
			say(w, "✓ Released %s · %s\nState:    %s %s\n", v.ID, v.Title, v.Status.Glyph(), stateLabel(v.Status))
			if v.Note != "" {
				say(w, "Note:     %s\n", v.Note)
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
	if err := c.parse(args); err != nil {
		return err
	}
	tok, err := c.token(ctx, c.args)
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
	return c.emit(rec, func(w io.Writer) { say(w, "✓ Logged entry #%d\n", rec.Seq) })
}

func runVerify(ctx context.Context, c *ctxt, args []string) error {
	reverify := c.fs.Bool("reverify", false, "complete only: run proof and regression again against the revision that completed the task; the task stays complete")
	if err := c.parse(args); err != nil {
		return err
	}
	if len(c.args) > 0 {
		if err := removedVerifyMode(c.args[0]); err != nil {
			return err
		}
	}
	if len(c.args) < 1 {
		return usage("verify needs a mode: `atask verify proof`, `atask verify regression`, `atask verify complete`, or `atask verify complete GROUP`")
	}
	if c.args[0] == string(verification.ModeComplete) && len(c.args) == 2 {
		if handled, err := c.verifyGroup(ctx, c.args[1]); handled {
			return err
		}
	}
	mode, err := verification.ParseMode(c.args[0])
	if err != nil {
		return usage("%v", err)
	}
	tok, err := c.token(ctx, c.args[1:])
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
	pid, e, isGroup := c.findGroup(ctx, ref)
	if !isGroup {
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

// findGroup resolves the project and engine when ref names a group. Any
// lookup failure means ref is not a group, so the caller tries it as a token.
func (c *ctxt) findGroup(ctx context.Context, ref string) (project.ID, *app.Engine, bool) {
	pid, err := c.resolveProject(ctx)
	if err != nil {
		return "", nil, false
	}
	e, err := c.engine(ctx)
	if err != nil {
		return "", nil, false
	}
	v, err := e.Show(ctx, pid, ref, false)
	return pid, e, err == nil && v.Kind == task.KindGroup
}

// looksLikeToken reports whether s has the shape of a session token, so a
// stray word is never mistaken for one.
func looksLikeToken(s string) bool {
	_, err := execution.ParseToken(s)
	return err == nil
}

// strayArgument explains an extra positional that is not a session token:
// usually an unquoted multi-word flag value or a task id.
func strayArgument(arg string) error {
	what := "an extra argument"
	if task.IsID(arg) {
		what = "a task id"
	}
	return usage("%q is %s, not a session token: quote flag values (--done \"two words\"); the token comes from the task worktree, ATASK_SESSION, or one positional TOKEN (or '-' for stdin); log and verify act on the claimed task and take no task id", arg, what)
}
