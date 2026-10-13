package proof

import (
	"fmt"
	"regexp"
	"strconv"
)

func init() { registerBuiltin(func() Preset { return rsTestPreset{} }) }

var (
	passedSummary = regexp.MustCompile(`test result: ok\. (\d+) passed`)
	_             = Preset(rsTestPreset{})
	_             = Judger(rsTestPreset{})
)

// rsTestPreset runs cargo test and passes only when a test actually ran.
type rsTestPreset struct{}

func (rsTestPreset) Name() string      { return "rs_test" }
func (rsTestPreset) Options() []string { return []string{"name"} }
func (rsTestPreset) DefaultSize() Size { return SizeMedium }

func (rsTestPreset) Argv(spec ProofSpec, _ Env) ([]string, error) {
	argv := []string{"cargo", "test"}
	if spec.Subject != "" {
		argv = append(argv, "-p", spec.Subject)
	}
	if name := spec.Options["name"]; name != "" {
		argv = append(argv, name)
	}
	return append(argv, "--", "--nocapture"), nil
}

func (rsTestPreset) Judge(_ ProofSpec, output []byte, exitCode int) (bool, string) {
	if exitCode != 0 {
		return false, fmt.Sprintf("exit code %d", exitCode)
	}
	for _, m := range passedSummary.FindAllSubmatch(output, -1) {
		if n, err := strconv.Atoi(string(m[1])); err == nil && n > 0 {
			return true, ""
		}
	}
	return false, "no tests ran"
}
