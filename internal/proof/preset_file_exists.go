package proof

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

var (
	_ = Preset(fileExistsPreset{})
	_ = Evaluator(fileExistsPreset{})
	_ = Validator(fileExistsPreset{})
)

func init() { registerBuiltin(func() Preset { return fileExistsPreset{} }) }

// fileExistsPreset passes when PATH exists inside the worktree.
type fileExistsPreset struct{}

func (fileExistsPreset) Name() string      { return "file_exists" }
func (fileExistsPreset) Options() []string { return nil }
func (fileExistsPreset) DefaultSize() Size { return SizeSmall }

func (fileExistsPreset) Argv(ProofSpec, Env) ([]string, error) { return nil, nil }

// Validate rejects a subject no claim worktree can satisfy, so it fails at
// add with the run: escape named instead of at verify.
func (fileExistsPreset) Validate(spec ProofSpec) error {
	return validateInsideWorktree("file_exists", spec.Subject, false)
}

func (fileExistsPreset) Evaluate(spec ProofSpec, workdir string) (bool, string, error) {
	rel, err := insideWorktree(spec.Subject)
	if err != nil {
		return false, "", err
	}
	_, err = os.Lstat(filepath.Join(workdir, rel))
	switch {
	case err == nil:
		return true, rel + " exists", nil
	case errors.Is(err, fs.ErrNotExist):
		return false, rel + " does not exist", nil
	default:
		return false, "", fault.Wrap(err, fault.CodeInvalidInput, "file_exists %q", spec.Subject)
	}
}

// insideWorktree returns the cleaned relative form of p, rejecting absolute
// paths and any ".." escape.
func insideWorktree(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || escapesWorktree(p) {
		return "", fault.New(fault.CodeInvalidInput, "path %q must be relative and stay inside the worktree", p)
	}
	return filepath.Clean(p), nil
}
