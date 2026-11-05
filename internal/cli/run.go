package cli

import (
	"context"
	"io"
	"os"
	"strconv"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

func init() {
	register("run", "inspect a verification run: atask run show ID (prints the run's log directory, resolved through the board)", runRun)
}

// runShown is what `atask run show` reports.
type runShown struct {
	RunID  int64  `json:"run_id"`
	LogDir string `json:"log_dir"`
	// LogsPresent is false when the directory is gone (pruned, or the run
	// executed on another machine).
	LogsPresent bool `json:"logs_present"`
}

func runRun(ctx context.Context, c *ctxt, args []string) error {
	if err := c.parse(args); err != nil {
		return err
	}
	if len(c.args) != 2 || c.args[0] != "show" {
		return usage("run needs a subcommand and a run id: `atask run show ID`")
	}
	id, err := strconv.ParseInt(c.args[1], 10, 64)
	if err != nil || id <= 0 {
		return fault.New(fault.CodeInvalidInput, "run id %q is not a positive number", c.args[1])
	}
	e, err := c.engine(ctx)
	if err != nil {
		return err
	}
	dir, err := e.RunLogDir(ctx, id)
	if err != nil {
		return err
	}
	_, statErr := os.Stat(dir)
	res := runShown{RunID: id, LogDir: dir, LogsPresent: statErr == nil}
	return c.emit(res, func(w io.Writer) {
		say(w, "%s\n", res.LogDir)
		if !res.LogsPresent {
			say(w, "(no logs on this machine for run %d)\n", id)
		}
	})
}
