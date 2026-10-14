package proof

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/fault"
)

var _ Evaluator = grepPreset{}

func init() { registerBuiltin(func() Preset { return grepPreset{} }) }

// grepPreset searches a file or directory tree for an RE2 pattern in-process.
type grepPreset struct{}

func (grepPreset) Name() string      { return "grep" }
func (grepPreset) Options() []string { return []string{"pattern", "absent"} }
func (grepPreset) DefaultSize() Size { return SizeSmall }

func (grepPreset) Argv(ProofSpec, Env) ([]string, error) {
	return nil, fault.New(fault.CodeInvalidInput, "grep is evaluated in-process")
}

func (grepPreset) Evaluate(spec ProofSpec, workdir string) (bool, string, error) {
	expr := spec.Options["pattern"]
	if expr == "" {
		return false, "", fault.New(fault.CodeInvalidInput, "grep %q needs pattern=REGEX", spec.Subject)
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return false, "", fault.Wrap(err, fault.CodeInvalidInput, "grep pattern %q", expr)
	}
	root, err := resolveInside(workdir, spec.Subject)
	if err != nil {
		return false, "", err
	}
	hit, err := firstMatch(workdir, root, re)
	if err != nil {
		return false, "", fault.Wrap(err, fault.CodeInvalidInput, "grep %q", spec.Subject)
	}
	absent := spec.Options["absent"] == "true"
	q := "pattern " + strconv.Quote(expr)
	switch {
	case hit != "":
		return !absent, q + " found at " + hit, nil
	case absent:
		return true, q + " absent from " + spec.Subject, nil
	}
	return false, q + " not found in " + spec.Subject, nil
}

// resolveInside joins rel to workdir and rejects any result outside it.
func resolveInside(workdir, rel string) (string, error) {
	abs := filepath.Join(workdir, rel)
	back, err := filepath.Rel(workdir, abs)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fault.New(fault.CodeInvalidInput, "grep path %q escapes the worktree", rel)
	}
	return abs, nil
}

// firstMatch returns "relpath:line" of the first matching line under root.
func firstMatch(workdir, root string, re *regexp.Regexp) (string, error) {
	var hit string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && d.Name() == ".git":
			return filepath.SkipDir
		case !d.Type().IsRegular():
			return nil
		}
		line, err := matchLine(p, re)
		if err != nil || line == 0 {
			return err
		}
		rel, _ := filepath.Rel(workdir, p)
		hit = filepath.ToSlash(rel) + ":" + strconv.Itoa(line)
		return fs.SkipAll
	})
	return hit, err
}

func matchLine(path string, re *regexp.Regexp) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer discard.CloseReadOnly(f)
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for n := 1; sc.Scan(); n++ {
		if re.Match(sc.Bytes()) {
			return n, nil
		}
	}
	return 0, sc.Err()
}
