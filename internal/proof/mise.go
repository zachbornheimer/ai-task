package proof

import (
	"os"
	"path/filepath"
)

// miseConfigFiles are the repo-relative files that mark a mise-managed worktree.
var miseConfigFiles = []string{"mise.toml", ".mise.toml", ".config/mise/config.toml", "mise.local.toml"}

// HasMiseConfig reports whether workdir holds a mise config file.
func HasMiseConfig(workdir string) bool {
	for _, rel := range miseConfigFiles {
		if _, err := os.Stat(filepath.Join(workdir, rel)); err == nil {
			return true
		}
	}
	return false
}

// wrapMise runs argv inside the repo's mise environment.
func wrapMise(argv []string) []string {
	return append([]string{"mise", "exec", "--"}, argv...)
}

// WithMiseDetector swaps how the Runner decides a worktree is mise-managed.
func WithMiseDetector(detect func(workdir string) bool) RunnerOption {
	return func(r *Runner) { r.hasMise = detect }
}
