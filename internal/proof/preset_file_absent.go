package proof

import (
	"io/fs"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

var (
	_ = Preset(fileAbsentPreset{})
	_ = Evaluator(fileAbsentPreset{})
)

func init() { registerBuiltin(func() Preset { return fileAbsentPreset{} }) }

// fileAbsentPreset passes when nothing in the worktree matches GLOB within
// depth directory levels. "**" matches any run of path segments.
type fileAbsentPreset struct{}

func (fileAbsentPreset) Name() string      { return "file_absent" }
func (fileAbsentPreset) Options() []string { return []string{"depth"} }
func (fileAbsentPreset) DefaultSize() Size { return SizeSmall }

func (fileAbsentPreset) Argv(ProofSpec, Env) ([]string, error) { return nil, nil }

func (fileAbsentPreset) Evaluate(spec ProofSpec, workdir string) (bool, string, error) {
	pattern, err := insideWorktree(spec.Subject)
	if err != nil {
		return false, "", err
	}
	maxDepth := -1
	if v, ok := spec.Options["depth"]; ok {
		if maxDepth, err = strconv.Atoi(v); err != nil || maxDepth < 1 {
			return false, "", fault.New(fault.CodeInvalidInput, "file_absent depth=%q must be a positive integer", v)
		}
	}
	segs := strings.Split(filepath.ToSlash(pattern), "/")
	found := ""
	_ = filepath.WalkDir(workdir, func(p string, d fs.DirEntry, werr error) error {
		rel, ok := relativeEntry(workdir, p, werr)
		if !ok {
			return nil
		}
		if matchSegments(segs, strings.Split(rel, "/")) {
			found = rel
			return fs.SkipAll
		}
		if d.IsDir() && maxDepth > 0 && strings.Count(rel, "/")+1 >= maxDepth {
			return fs.SkipDir
		}
		return nil
	})
	if found != "" {
		return false, found + " matches " + pattern, nil
	}
	return true, "nothing matches " + pattern, nil
}

func matchSegments(pat, name []string) bool {
	if len(pat) == 0 {
		return len(name) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(name); i++ {
			if matchSegments(pat[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], name[0])
	return err == nil && ok && matchSegments(pat[1:], name[1:])
}

// relativeEntry is the slash-separated path of a walked entry below workdir.
// It is false for the root itself and for entries the walk could not read,
// which cannot match a pattern.
func relativeEntry(workdir, path string, walkErr error) (string, bool) {
	if walkErr != nil {
		return "", false
	}
	rel, err := filepath.Rel(workdir, path)
	if err != nil || rel == "." {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
