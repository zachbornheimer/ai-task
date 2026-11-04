package proof

import (
	"path/filepath"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// escapesWorktree reports whether p is absolute or climbs out of its base
// directory.
func escapesWorktree(p string) bool {
	clean := filepath.Clean(p)
	return filepath.IsAbs(p) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

// validateInsideWorktree is the parse-time rule of the check presets whose
// subjects name a path or glob under the claim worktree: a subject that is
// absolute or climbs out can never pass at verify, so it is INVALID_INPUT
// at add, naming the run: escape that checks a path outside the worktree.
// An empty subject is allowed where the preset reads it as the worktree
// itself.
func validateInsideWorktree(preset, subject string, emptyIsWorktree bool) error {
	p := strings.TrimSpace(subject)
	if p == "" {
		if emptyIsWorktree {
			return nil
		}
		return fault.New(fault.CodeInvalidInput, "%s needs a path relative to the claim worktree", preset)
	}
	if escapesWorktree(p) {
		return fault.New(fault.CodeInvalidInput,
			"%s: path %q must be relative and stay inside the claim worktree; to check a path outside it, use \"run: COMMAND\" (e.g. run: test -f %s)", preset, p, p)
	}
	return nil
}
