package proof

import (
	"bufio"
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

func init() { registerBuiltin(func() Preset { return goTestPreset{} }) }

const reasonNamedTestMissing = "named test did not run"

var _ interface {
	Preset
	Judger
	Validator
} = goTestPreset{}

// goTestPreset proves a Go package's tests pass. With name=REGEX it also
// proves a matching test ran, which `go test -run` alone does not: a regex
// matching nothing exits 0.
type goTestPreset struct{}

func (goTestPreset) Name() string      { return "go_test" }
func (goTestPreset) Options() []string { return []string{"name"} }
func (goTestPreset) DefaultSize() Size { return SizeSmall }

func (goTestPreset) Argv(spec ProofSpec, _ Env) ([]string, error) {
	pkg := strings.TrimSpace(spec.Subject)
	if pkg == "" {
		return nil, fault.New(fault.CodeInvalidInput, "go_test needs a package, e.g. go_test: ./internal/proof")
	}
	argv := []string{"go", "test", "-count=1", "-json", pkg}
	if name := spec.Options["name"]; name != "" {
		argv = append(argv, "-run", name)
	}
	return argv, nil
}

// Validate rejects a whole-tree run with no name= unless it is sized past small.
func (goTestPreset) Validate(spec ProofSpec) error {
	if strings.Contains(spec.Subject, "...") && spec.Options["name"] == "" && spec.Size == SizeSmall {
		return fault.New(fault.CodeInvalidInput,
			"go_test %q covers a whole tree in a small proof; add name=REGEX or name a package", spec.Subject)
	}
	return nil
}

type testEvent struct {
	Action string
	Test   string
}

// Judge passes when no event failed and, with name=, a matching test passed.
func (goTestPreset) Judge(spec ProofSpec, output []byte, exitCode int) (bool, string) {
	var re *regexp.Regexp
	if name := spec.Options["name"]; name != "" {
		var err error
		if re, err = regexp.Compile(name); err != nil {
			return false, "name= is not a valid regex: " + err.Error()
		}
	}
	matched := false
	sc := bufio.NewScanner(bytes.NewReader(output))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var ev testEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Action {
		case "fail":
			return false, "go test reported a fail event"
		case "pass":
			matched = matched || (re != nil && ev.Test != "" && re.MatchString(ev.Test))
		}
	}
	if exitCode != 0 {
		return false, "go test exited non-zero"
	}
	if re != nil && !matched {
		return false, reasonNamedTestMissing
	}
	return true, ""
}
