// Package cli parses arguments and renders results. It contains no domain
// rules: every decision is made by the engine and reported through stable
// error codes and JSON envelopes.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
)

// Version is set by the build; "dev" otherwise.
var Version = "dev"

// Exit codes. Domain failures carry a code in the envelope; the process exit
// status only distinguishes success, usage errors, and everything else.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// Env is the process environment the CLI depends on, injected for tests.
type Env struct {
	Args   []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	Cwd    string
}

// globals are options every subcommand accepts, from flags or environment.
type globals struct {
	db      string
	project string
	// projects are the --project flags given after add or update, in
	// order; the first is the home. Empty otherwise: a --project before the
	// command only scopes it.
	projects []string
	json     bool
}

// repeatableCommands accept --project more than once.
var repeatableCommands = map[string]bool{"add": true, "update": true}

// projectFlag binds --project: a single value everywhere, a repeatable
// list (first is the home) on add and update.
type projectFlag struct {
	g        *globals
	repeated bool
}

func (f *projectFlag) String() string {
	if f == nil || f.g == nil {
		return ""
	}
	return f.g.project
}

func (f *projectFlag) Set(v string) error {
	f.g.project = v
	if f.repeated {
		f.g.projects = append(f.g.projects, v)
		f.g.project = f.g.projects[0]
	}
	return nil
}

type command struct {
	name    string
	summary string
	run     func(ctx context.Context, c *ctxt, args []string) error
}

// ctxt is the per-invocation context handed to commands.
type ctxt struct {
	env  Env
	g    globals
	fs   *flag.FlagSet
	name string      // the subcommand being run
	eng  *app.Engine // opened lazily
	args []string    // positional arguments after interleaved parsing
}

// Main runs the CLI and returns the process exit code.
func Main(env Env) int {
	if env.Getenv == nil {
		env.Getenv = os.Getenv
	}
	if env.Cwd == "" {
		env.Cwd, _ = os.Getwd()
	}
	ctx := context.Background()
	c := &ctxt{env: env}
	c.g = globals{db: env.Getenv("ATASK_DB"), project: env.Getenv("ATASK_PROJECT"), json: strings.EqualFold(env.Getenv("ATASK_OUTPUT"), "json")}

	top := flag.NewFlagSet("atask", flag.ContinueOnError)
	top.SetOutput(io.Discard)
	c.bindGlobals(top)
	if err := top.Parse(env.Args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// `atask --help` asks for the command list: same as `atask help`.
			if err := commands["help"].run(ctx, c, nil); err != nil {
				return c.fail(err)
			}
			return ExitOK
		}
		return c.usageError(removedFlagError(anyCommand, err))
	}
	rest := top.Args()
	if len(rest) == 0 {
		rest = []string{"list"}
	}
	name, args := rest[0], rest[1:]
	cmd, ok := commands[name]
	if !ok {
		takeJSONMode(&c.g, args)
		return c.usageError(removedVerbError(name))
	}
	c.name = name
	c.fs = flag.NewFlagSet(name, flag.ContinueOnError)
	c.fs.SetOutput(io.Discard)
	c.bindGlobals(c.fs)
	err := cmd.run(ctx, c, args)
	if c.eng != nil {
		if closeErr := c.eng.Close(); err == nil {
			err = closeErr
		}
	}
	if _, ok := errors.AsType[*helpRequest](err); ok {
		if err := c.printCommandHelp(cmd); err != nil {
			return c.fail(err)
		}
		return ExitOK
	}
	if err != nil {
		return c.fail(err)
	}
	return ExitOK
}

func (c *ctxt) bindGlobals(fs *flag.FlagSet) {
	dbHelp := "database path or postgres URL (env ATASK_DB_URL, ATASK_DB)"
	if def, err := app.DefaultPath(); err == nil && c.g.db == "" {
		// Redacted: a postgres URL carries credentials and must never
		// reach output, not even a help string.
		dbHelp = "database path or postgres URL (env ATASK_DB_URL, ATASK_DB; default " + sqlite.Redacted(def) + ")"
	}
	fs.StringVar(&c.g.db, "db", c.g.db, dbHelp)
	help := "project id or name; names match ignoring case and punctuation (env ATASK_PROJECT)"
	if repeatableCommands[c.name] {
		help = "project id or name; names match ignoring case and punctuation (env ATASK_PROJECT); repeat to name more projects, the first is the home"
	}
	fs.Var(&projectFlag{g: &c.g, repeated: c.fs == fs && repeatableCommands[c.name]}, "project", help)
	fs.BoolVar(&c.g.json, "json", c.g.json, "JSON output (env ATASK_OUTPUT=json)")
}

// encoder writes JSON compactly: agents read it, and a one-line envelope
// costs fewer tokens than an indented one.
func (c *ctxt) encoder() *json.Encoder { return json.NewEncoder(c.env.Stdout) }

func (c *ctxt) jsonOut() bool { return c.g.json }

// takeJSONMode applies a trailing --json after an unknown command, which
// top.Parse never reaches (it stops at the first non-flag).
func takeJSONMode(g *globals, args []string) {
	for _, a := range args {
		if a == "--json" || a == "-json" {
			g.json = true
		}
	}
}

// parse parses flags and positionals interleaved in any order, so both
// `tasks deps add B --requires A` and `tasks deps add --requires A B` work.
func (c *ctxt) parse(args []string) error {
	var pos []string
	for {
		if err := c.fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return &helpRequest{}
			}
			return removedFlagError(c.name, err)
		}
		rest := c.fs.Args()
		// flag.Parse consumes a "--" terminator itself; if the last consumed
		// argument was one, everything after it is positional.
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			pos = append(pos, rest...)
			break
		}
		if len(rest) == 0 {
			break
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
	c.args = pos
	return nil
}

// helpRequest is returned by parse for --help / -h: the command's flags are
// declared by then, so Main prints them and exits 0.
type helpRequest struct{}

func (*helpRequest) Error() string { return "help requested" }

// printCommandHelp prints the command's usage line and every flag with its
// description: on stdout, or as a JSON envelope.
func (c *ctxt) printCommandHelp(cmd command) error { return c.printCommandHelpWith(cmd, nil) }

// printCommandHelpWith is printCommandHelp followed by a preset table.
func (c *ctxt) printCommandHelpWith(cmd command, presets []presetRow) error {
	type flagHelp struct {
		Name    string `json:"name"`
		Usage   string `json:"usage"`
		Default string `json:"default,omitempty"`
	}
	type commandHelp struct {
		Command string      `json:"command"`
		Usage   string      `json:"usage"`
		Flags   []flagHelp  `json:"flags"`
		Presets []presetRow `json:"presets,omitempty"`
	}
	h := commandHelp{Command: cmd.name, Usage: cmd.summary, Presets: presets}
	c.fs.VisitAll(func(f *flag.Flag) {
		h.Flags = append(h.Flags, flagHelp{f.Name, f.Usage, f.DefValue})
	})
	return c.emit(h, func(w io.Writer) {
		say(w, "Usage: atask %s\n  %s\n\nFlags:\n", cmd.name, cmd.summary)
		c.fs.SetOutput(w)
		c.fs.PrintDefaults()
		if presets != nil {
			renderPresets(w, presets)
		}
	})
}

type usageErr struct{ err error }

func (u *usageErr) Error() string { return u.err.Error() }

func usage(format string, a ...any) error { return &usageErr{fmt.Errorf(format, a...)} }

// engine opens the store on first use.
func (c *ctxt) engine(ctx context.Context) (*app.Engine, error) {
	if c.eng != nil {
		return c.eng, nil
	}
	actor := c.env.Getenv("ATASK_ACTOR")
	if actor == "" {
		actor = c.env.Getenv("USER")
	}
	e, err := app.Open(ctx, app.Config{Path: c.g.db, Actor: actor, Cwd: c.env.Cwd})
	if err != nil {
		return nil, err
	}
	c.eng = e
	return e, nil
}

// resolveProject applies --project / TASKS_PROJECT, then the working
// directory.
func (c *ctxt) resolveProject(ctx context.Context) (project.ID, error) {
	e, err := c.engine(ctx)
	if err != nil {
		return "", err
	}
	p, err := e.ResolveProject(ctx, c.g.project, c.env.Cwd)
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

// envelope is the stable JSON output shape.
type envelope struct {
	OK     bool      `json:"ok"`
	Result any       `json:"result,omitempty"`
	Error  *errorOut `json:"error,omitempty"`
}

type errorOut struct {
	Code    fault.Code `json:"code"`
	Message string     `json:"message"`
	// Details carries a structured result for failures that still recorded
	// facts, e.g. the verification run behind CHECK_FAILED.
	Details any `json:"details,omitempty"`
}

// emit renders a successful result: JSON envelope or the human renderer.
func (c *ctxt) emit(result any, human func(w io.Writer)) error {
	if c.jsonOut() {
		return c.encoder().Encode(envelope{OK: true, Result: result})
	}
	out := &errWriter{w: c.env.Stdout}
	human(out)
	if out.err != nil {
		return fmt.Errorf("write output: %w", out.err)
	}
	return nil
}

func (c *ctxt) fail(err error) int {
	var u *usageErr
	if isUsage(err, &u) {
		return c.usageError(u.err)
	}
	code := fault.CodeOf(err)
	msg := err.Error()
	var details any
	var fe *fault.Error
	if asFault(err, &fe) {
		msg = fe.Message
		// Tooling failures carry the tool's own words (git's stderr).
		if fe.Err != nil && (fault.IsInternal(err) || code == fault.CodeNotReady || code == fault.CodeBaseConflict) && !strings.Contains(msg, fe.Err.Error()) {
			msg += ": " + fe.Err.Error()
		}
		details = fe.Details
	}
	if c.jsonOut() {
		_ = c.encoder().Encode(envelope{OK: false, Error: &errorOut{Code: code, Message: msg, Details: details}})
	} else {
		say(c.env.Stderr, "error [%s]: %s\n", code, msg)
		if vr, ok := details.(app.VerifyResult); ok {
			renderEvidence(c.env.Stderr, vr.Evidence, false)
			if vr.Message != "" {
				say(c.env.Stderr, "  %s\n", vr.Message)
			}
			if vr.LogDir != "" {
				say(c.env.Stderr, "  logs: %s (result.json and one log per check)\n", vr.LogDir)
			}
		}
		if rep, ok := details.(app.DoctorReport); ok {
			renderDoctor(c.env.Stderr, rep)
		}
		if sum, ok := details.(app.Summary); ok {
			for _, r := range sum.Reasons {
				say(c.env.Stderr, "  %s\n", r)
			}
		}
	}
	return ExitError
}

func (c *ctxt) usageError(err error) int {
	if c.jsonOut() {
		_ = c.encoder().Encode(envelope{OK: false, Error: &errorOut{Code: fault.CodeInvalidInput, Message: "usage: " + err.Error()}})
	} else {
		say(c.env.Stderr, "usage error: %s\nRun `atask help` for commands.\n", err)
	}
	return ExitUsage
}

func (c *ctxt) printHelp(w io.Writer) {
	sayln(w, "atask - agent task engine: atomic plans, leased claims, verified completion")
	sayln(w, "\nUsage: atask [--db PATH] [--project ID|NAME] [--json] [command [args]]")
	sayln(w, "       no command lists every non-archived task (same as atask list)")
	sayln(w, `
atask context                         operating contract for agents (no project or database)
atask context ID                      worker handoff for that task
atask help [command]                  this reference, or one command's flags
atask version                         build version

atask add "title" --proof "run: cmd" --regression "run: cmd" [--parent ID] [--requires ID] [--group]
atask update ID [--proof ...] [--regression ...] [--archive --reason ...]
atask list [ready|blocked|complete|archived|open]
atask show [ID] [--full]
atask claim [ID] [--wait]
atask claim --group REF [--wait]
atask claim renew [token]
atask claim release [token] [--note ...] [--failed]
atask log --done ... --next ... --learned ... --note ...
atask verify proof|regression|complete

Commands:`)
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		say(w, "  %-18s %s\n", n, commands[n].summary)
	}
	sayln(w, "\nEnvironment: ATASK_DB_URL (postgres URL for shared state), ATASK_DB (local SQLite path), ATASK_PROJECT, ATASK_OUTPUT=json, ATASK_SESSION (optional inside a task worktree: the token is stored there)")
}

var commands = map[string]command{}

func register(name, summary string, run func(ctx context.Context, c *ctxt, args []string) error) {
	commands[name] = command{name: name, summary: summary, run: run}
}

func init() {
	register("help", "show this help", func(_ context.Context, c *ctxt, args []string) error {
		if c.fs != nil {
			if err := c.parse(args); err != nil {
				return err
			}
		}
		if len(c.args) == 1 {
			return runCommandHelp(c, c.args[0])
		}
		type cmd struct {
			Name    string `json:"name"`
			Summary string `json:"summary"`
		}
		type help struct {
			Usage       string   `json:"usage"`
			Commands    []cmd    `json:"commands"`
			Environment []string `json:"environment"`
		}
		names := make([]string, 0, len(commands))
		for n := range commands {
			names = append(names, n)
		}
		sort.Strings(names)
		h := help{Usage: "atask [--db PATH] [--project ID|NAME] [--json] [command [args]]", Environment: []string{"ATASK_DB_URL", "ATASK_DB", "ATASK_PROJECT", "ATASK_OUTPUT=json", "ATASK_SESSION"}}
		for _, n := range names {
			h.Commands = append(h.Commands, cmd{n, commands[n].summary})
		}
		return c.emit(h, func(w io.Writer) { c.printHelp(w) })
	})
	register("version", "print the version", func(_ context.Context, c *ctxt, args []string) error {
		if err := c.parse(args); err != nil {
			return err
		}
		v := buildVersion(Version, debug.ReadBuildInfo)
		return c.emit(map[string]string{"version": v}, func(w io.Writer) { sayln(w, v) })
	})
}

// buildVersion is the version `atask version` reports: the one set at link
// time, else the module version `go install` recorded (a tag or a
// pseudo-version naming the commit), else "dev" with the VCS revision of
// a source build.
func buildVersion(linked string, read func() (*debug.BuildInfo, bool)) string {
	if linked != "dev" && linked != "" {
		return linked
	}
	bi, ok := read()
	if !ok {
		return "dev"
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	rev, dirty := "", false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "-dirty"
	}
	return "dev+" + rev
}

// runCommandHelp is `atask help NAME`: what `atask NAME --help` prints (the
// command's own flags and the globals), plus the preset table for `add`.
func runCommandHelp(c *ctxt, name string) error {
	cmd, ok := commands[name]
	if !ok {
		return usage("unknown command %q", name)
	}
	sub := &ctxt{env: c.env, g: c.g, name: name}
	sub.fs = flag.NewFlagSet(name, flag.ContinueOnError)
	sub.fs.SetOutput(io.Discard)
	sub.bindGlobals(sub.fs)
	var h *helpRequest
	if err := cmd.run(context.Background(), sub, []string{"--help"}); !errors.As(err, &h) {
		return err
	}
	if name != "add" {
		return sub.printCommandHelp(cmd)
	}
	rows, err := addHelpPresets()
	if err != nil {
		return err
	}
	return sub.printCommandHelpWith(cmd, rows)
}
