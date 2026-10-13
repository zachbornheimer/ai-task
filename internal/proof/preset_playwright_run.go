package proof

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

func init() { registerBuiltin(func() Preset { return playwrightRunPreset{} }) }

// playwrightRunPreset runs a Playwright spec with the JSON reporter and
// judges the report: at least one test ran and none failed or flaked.
type playwrightRunPreset struct{}

func (playwrightRunPreset) Name() string      { return "playwright_run" }
func (playwrightRunPreset) Options() []string { return []string{"grep", "project"} }
func (playwrightRunPreset) DefaultSize() Size { return SizeMedium }

func (playwrightRunPreset) Argv(spec ProofSpec, _ Env) ([]string, error) {
	if strings.TrimSpace(spec.Subject) == "" {
		return nil, fault.New(fault.CodeInvalidInput, "playwright_run needs a spec path")
	}
	argv := []string{"npx", "playwright", "test", spec.Subject, "--reporter=json"}
	if g := spec.Options["grep"]; g != "" {
		argv = append(argv, "--grep", g)
	}
	if p := spec.Options["project"]; p != "" {
		argv = append(argv, "--project", p)
	}
	return argv, nil
}

type playwrightReport struct {
	Stats struct {
		Expected   int `json:"expected"`
		Unexpected int `json:"unexpected"`
		Flaky      int `json:"flaky"`
	} `json:"stats"`
}

func (playwrightRunPreset) Judge(_ ProofSpec, output []byte, _ int) (bool, string) {
	var r playwrightReport
	if start := bytes.IndexByte(output, '{'); start > 0 {
		output = output[start:]
	}
	if err := json.Unmarshal(output, &r); err != nil {
		return false, "playwright_run: stdout is not a JSON report: " + err.Error()
	}
	s := r.Stats
	switch {
	case s.Expected == 0:
		return false, "playwright_run: no tests ran"
	case s.Unexpected > 0 || s.Flaky > 0:
		return false, "playwright_run: " + strconv.Itoa(s.Unexpected) + " failed, " + strconv.Itoa(s.Flaky) + " flaky"
	}
	return true, ""
}
