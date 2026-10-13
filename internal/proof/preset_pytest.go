package proof

import (
	"fmt"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

func init() { registerBuiltin(func() Preset { return pyTestPreset{} }) }

// pytestNoTestsCollected is pytest's exit code when nothing was collected.
const pytestNoTestsCollected = 5

var _ interface {
	Preset
	Judger
} = pyTestPreset{}

// pyTestPreset runs pytest on a path, optionally filtered by -k name=EXPR.
type pyTestPreset struct{}

func (pyTestPreset) Name() string      { return "py_test" }
func (pyTestPreset) Options() []string { return []string{"name"} }
func (pyTestPreset) DefaultSize() Size { return SizeSmall }

func (pyTestPreset) Argv(spec ProofSpec, _ Env) ([]string, error) {
	if spec.Subject == "" {
		return nil, fault.New(fault.CodeInvalidInput, "py_test needs a path")
	}
	argv := []string{"pytest", spec.Subject, "-q"}
	if expr := spec.Options["name"]; expr != "" {
		argv = append(argv, "-k", expr)
	}
	return argv, nil
}

func (pyTestPreset) Judge(_ ProofSpec, _ []byte, exitCode int) (bool, string) {
	switch exitCode {
	case 0:
		return true, ""
	case pytestNoTestsCollected:
		return false, "no tests collected"
	default:
		return false, fmt.Sprintf("pytest exited %d", exitCode)
	}
}
