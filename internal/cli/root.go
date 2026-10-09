// Package cli parses arguments and renders results. It contains no domain
// rules: every decision is made by the engine and reported through stable
// error codes and JSON envelopes.
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/app"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
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
	json    bool
	output  string
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
	c.g = globals{db: env.Getenv("TASKS_DB"), project: env.Getenv("TASKS_PROJECT"), output: env.Getenv("TASKS_OUTPUT")}

	top := flag.NewFlagSet("tasks", flag.ContinueOnError)
	top.SetOutput(io.Discard)
	c.bindGlobals(top)
	if err := top.Parse(env.Args); err != nil {
		return c.usageError(err)
	}
	rest := top.Args()
	if len(rest) == 0 {
		c.printHelp()
		return ExitUsage
	}
	name, args := rest[0], rest[1:]
	cmd, ok := commands[name]
	if !ok {
		return c.usageError(fmt.Errorf("unknown command %q", name))
	}
	c.fs = flag.NewFlagSet(name, flag.ContinueOnError)
	c.fs.SetOutput(io.Discard)
	c.bindGlobals(c.fs)
	err := cmd.run(ctx, c, args)
	if c.eng != nil {
		c.eng.Close()
	}
	if err != nil {
		return c.fail(err)
	}
	return ExitOK
}

func (c *ctxt) bindGlobals(fs *flag.FlagSet) {
	fs.StringVar(&c.g.db, "db", c.g.db, "database path (env TASKS_DB)")
	fs.StringVar(&c.g.project, "project", c.g.project, "project id or name (env TASKS_PROJECT)")
	fs.BoolVar(&c.g.json, "json", c.g.json, "JSON output (env TASKS_OUTPUT=json)")
	fs.StringVar(&c.g.output, "output", c.g.output, "output format: text|json")
}

func (c *ctxt) jsonOut() bool { return c.g.json || strings.EqualFold(c.g.output, "json") }

// parse parses flags and positionals interleaved in any order, so both
// `tasks deps add B --requires A` and `tasks deps add --requires A B` work.
func (c *ctxt) parse(args []string) error {
	var pos []string
	for {
		if err := c.fs.Parse(args); err != nil {
			return &usageErr{err}
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

type usageErr struct{ err error }

func (u *usageErr) Error() string { return u.err.Error() }

func usage(format string, a ...any) error { return &usageErr{fmt.Errorf(format, a...)} }

// engine opens the store on first use.
func (c *ctxt) engine(ctx context.Context) (*app.Engine, error) {
	if c.eng != nil {
		return c.eng, nil
	}
	e, err := app.Open(ctx, app.Config{Path: c.g.db})
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
	// facts, e.g. the verification run behind VERIFICATION_FAILED.
	Details any `json:"details,omitempty"`
}

// emit renders a successful result: JSON envelope or the human renderer.
func (c *ctxt) emit(result any, human func(w io.Writer)) error {
	if c.jsonOut() {
		enc := json.NewEncoder(c.env.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(envelope{OK: true, Result: result})
	}
	human(c.env.Stdout)
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
		if fe.Err != nil && code == fault.CodeInternal {
			msg += ": " + fe.Err.Error()
		}
		details = fe.Details
	}
	if c.jsonOut() {
		enc := json.NewEncoder(c.env.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(envelope{OK: false, Error: &errorOut{Code: code, Message: msg, Details: details}})
	} else {
		fmt.Fprintf(c.env.Stderr, "error [%s]: %s\n", code, msg)
		if vr, ok := details.(app.VerifyResult); ok {
			renderEvidence(c.env.Stderr, vr.Evidence, false)
		}
		if sr, ok := details.(app.SubmissionResult); ok && sr.RunID != 0 {
			fmt.Fprintf(c.env.Stderr, "run %d: %s\n", sr.RunID, sr.Summary)
		}
	}
	return ExitError
}

func (c *ctxt) usageError(err error) int {
	if c.jsonOut() {
		enc := json.NewEncoder(c.env.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(envelope{OK: false, Error: &errorOut{Code: fault.CodeInvalidInput, Message: "usage: " + err.Error()}})
	} else {
		fmt.Fprintf(c.env.Stderr, "usage error: %s\nRun `tasks help` for commands.\n", err)
	}
	return ExitUsage
}

func (c *ctxt) printHelp() {
	w := c.env.Stderr
	fmt.Fprintln(w, "tasks - durable task engine for coding agents")
	fmt.Fprintln(w, "\nUsage: tasks [--db PATH] [--project ID|NAME] [--json] <command> [args]")
	fmt.Fprintln(w, "\nCommands:")
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "  %-18s %s\n", n, commands[n].summary)
	}
	fmt.Fprintln(w, "\nEnvironment: TASKS_DB, TASKS_PROJECT, TASKS_OUTPUT=json, TASKS_SESSION")
	fmt.Fprintln(w, "Dependency direction: `tasks deps add B --requires A` means B requires A.")
}

var commands = map[string]command{}

func register(name, summary string, run func(ctx context.Context, c *ctxt, args []string) error) {
	commands[name] = command{name: name, summary: summary, run: run}
}

func init() {
	register("help", "show this help", func(_ context.Context, c *ctxt, _ []string) error {
		c.printHelp()
		return nil
	})
	register("version", "print the version", func(_ context.Context, c *ctxt, _ []string) error {
		return c.emit(map[string]string{"version": Version}, func(w io.Writer) { fmt.Fprintln(w, Version) })
	})
}
