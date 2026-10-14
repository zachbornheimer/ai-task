package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
)

// keepRunLogs is how many run directories each project keeps; older ones
// are removed when a new run starts writing.
const keepRunLogs = 100

// runLogDir is where run runID of project pid writes its check logs and
// result: <log root>/<project>/run-<id>.
func (e *Engine) runLogDir(pid project.ID, runID int64) string {
	if runID == 0 {
		return ""
	}
	return filepath.Join(e.logRoot, string(pid), fmt.Sprintf("run-%d", runID))
}

// openCheckLog creates a log file for checkID in dir, creating dir (and
// pruning the project's oldest runs) the first time. A check that runs
// twice in one run (the submitted revision, then the merge) gets
// check.2.log. Logs are a convenience: any failure returns nil and the
// check runs without one.
func openCheckLog(dir, checkID string) (*os.File, string) {
	if dir == "" {
		return nil, ""
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, ""
		}
		pruneRunLogs(filepath.Dir(dir), keepRunLogs)
	}
	name := strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '_'
	}, checkID)
	for i := 1; i < 100; i++ {
		file := name + ".log"
		if i > 1 {
			file = fmt.Sprintf("%s.%d.log", name, i)
		}
		path := filepath.Join(dir, file)
		f, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return f, path
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, ""
		}
	}
	return nil, ""
}

// pruneRunLogs keeps the newest keep run directories in projDir.
func pruneRunLogs(projDir string, keep int) {
	entries, err := os.ReadDir(projDir)
	if err != nil {
		return
	}
	var runs []int64
	for _, ent := range entries {
		if id, err := strconv.ParseInt(strings.TrimPrefix(ent.Name(), "run-"), 10, 64); err == nil && ent.IsDir() && strings.HasPrefix(ent.Name(), "run-") {
			runs = append(runs, id)
		}
	}
	if len(runs) <= keep {
		return
	}
	slices.Sort(runs)
	for _, id := range runs[:len(runs)-keep] {
		_ = os.RemoveAll(filepath.Join(projDir, fmt.Sprintf("run-%d", id)))
	}
}

// runResult is what result.json holds: the verify outcome in the shape of
// the CLI's JSON envelope.
type runResult struct {
	OK     bool         `json:"ok"`
	Result VerifyResult `json:"result"`
	Error  *runError    `json:"error,omitempty"`
}

type runError struct {
	Code    fault.Code `json:"code"`
	Message string     `json:"message"`
}

// writeRunResult records a verification's outcome as result.json in its
// run directory, so the whole result can be read after the command's
// output is gone. A replayed result already has its file.
func writeRunResult(res VerifyResult, err error) {
	if res.LogDir == "" || res.Replayed {
		return
	}
	out := runResult{OK: err == nil, Result: res}
	if err != nil {
		out.Error = &runError{Code: fault.CodeOf(err), Message: fault.MessageOf(err)}
	}
	b, merr := json.MarshalIndent(out, "", "  ")
	if merr != nil {
		return
	}
	if os.MkdirAll(res.LogDir, 0o750) != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(res.LogDir, "result.json"), append(b, '\n'), 0o600)
}
