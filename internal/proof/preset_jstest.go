package proof

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// The file is not named preset_js_test.go: that suffix makes Go treat it as
// test-only code, which would leave js_test out of the shipped binary.

var (
	zeroTests = regexp.MustCompile(`No tests found|\b0 passed|Tests:\s+0 total`)
	_         = Preset(jsTestPreset{})
	_         = Judger(jsTestPreset{})
)

func init() { registerBuiltin(func() Preset { return jsTestPreset{} }) }

// jsTestPreset runs the project's own test script, filtered to one path and
// optionally one test name.
type jsTestPreset struct{}

func (jsTestPreset) Name() string      { return "js_test" }
func (jsTestPreset) Options() []string { return []string{"name"} }
func (jsTestPreset) DefaultSize() Size { return SizeSmall }

func (jsTestPreset) Argv(spec ProofSpec, env Env) ([]string, error) {
	if strings.TrimSpace(spec.Subject) == "" {
		return nil, fault.New(fault.CodeInvalidInput, "js_test needs a test path")
	}
	argv := append(packageManagerTest(env.Workdir), spec.Subject)
	if name := spec.Options["name"]; name != "" {
		argv = append(argv, "-t", name)
	}
	return argv, nil
}

// Judge passes only when the run exited 0 and did not report zero tests.
func (jsTestPreset) Judge(_ ProofSpec, output []byte, exitCode int) (bool, string) {
	switch {
	case exitCode != 0:
		return false, "test script exited non-zero"
	case zeroTests.Match(output):
		return false, "no tests ran"
	}
	return true, ""
}

// packageManagerTest picks the test command from the lockfile in dir.
func packageManagerTest(dir string) []string {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	switch {
	case has("pnpm-lock.yaml"):
		return []string{"pnpm", "test", "--"}
	case has("yarn.lock"):
		return []string{"yarn", "test"}
	case has("bun.lockb"), has("bun.lock"):
		return []string{"bun", "test"}
	}
	return []string{"npm", "test", "--"}
}
