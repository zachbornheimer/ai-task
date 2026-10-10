package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zachbornheimer/ai-task/internal/plan"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

// planItem is one task or group in a plan file. The field names are the
// ones `at show` prints, so a planner can read the plan out and write it
// back in the same vocabulary.
type planItem struct {
	Kind           string      `json:"kind"` // task (default) | group
	Key            string      `json:"key"`
	Title          string      `json:"title"`
	Outcome        string      `json:"outcome"`
	Parent         string      `json:"parent"`
	Cohort         string      `json:"cohort"`
	Constraints    []string    `json:"constraints"`
	Acceptance     []string    `json:"acceptance"`
	Requires       []string    `json:"requires"`
	Blocks         []string    `json:"blocks"`
	Checks         []checkItem `json:"checks"`
	OptionalChecks []checkItem `json:"optional_checks"`
	Pins           []string    `json:"pins"`
	Size           string      `json:"size"`
}

// checkItem is a check as a plan file writes it: a string in the
// `[id:] command args...` form, or an object with id, command (a string
// or an argv list), dir, timeout_ms and required.
type checkItem struct {
	ID        string
	Command   []string
	Dir       string
	TimeoutMS int64
	Required  *bool
	raw       string
}

func (c *checkItem) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		c.raw = s
		return nil
	}
	var obj struct {
		ID        string          `json:"id"`
		Command   json.RawMessage `json:"command"`
		Dir       string          `json:"dir"`
		TimeoutMS int64           `json:"timeout_ms"`
		Required  *bool           `json:"required"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	c.ID, c.Dir, c.TimeoutMS, c.Required = obj.ID, obj.Dir, obj.TimeoutMS, obj.Required
	var argv []string
	if err := json.Unmarshal(obj.Command, &argv); err == nil {
		c.Command = argv
		return nil
	}
	var cmd string
	if err := json.Unmarshal(obj.Command, &cmd); err != nil {
		return fmt.Errorf("check command must be a string or a list of strings")
	}
	c.raw = cmd
	return nil
}

func (c checkItem) spec(n int, required bool) (verification.CheckSpec, error) {
	if c.raw != "" && len(c.Command) == 0 {
		cs, err := parseCheck(c.raw, n, required)
		if err != nil {
			return cs, err
		}
		if c.ID != "" {
			cs.ID = c.ID
		}
		if c.Dir != "" {
			cs.Dir = c.Dir
		}
		if c.TimeoutMS > 0 {
			cs.Timeout = time.Duration(c.TimeoutMS) * time.Millisecond
		}
		if c.Required != nil {
			cs.Required = *c.Required
		}
		return cs, nil
	}
	if len(c.Command) == 0 {
		return verification.CheckSpec{}, usage("check %d: a command is required", n)
	}
	id := c.ID
	if id == "" {
		id = fmt.Sprintf("check%d", n)
	}
	cs := verification.CheckSpec{ID: id, Command: c.Command, Dir: c.Dir, Required: required}
	if c.TimeoutMS > 0 {
		cs.Timeout = time.Duration(c.TimeoutMS) * time.Millisecond
	}
	if c.Required != nil {
		cs.Required = *c.Required
	}
	return cs, nil
}

// readPlanItems decodes a plan from JSON or YAML: one item, a list of
// items, or an object with a `tasks` (or `items`) list. YAML is decoded
// to generic values and re-encoded as JSON so one set of field names
// serves both formats.
func readPlanItems(name string, r io.Reader) ([]planItem, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, usage("read %s: %v", name, err)
	}
	if strings.TrimSpace(string(b)) == "" {
		return nil, usage("%s is empty", name)
	}
	var generic any
	if strings.HasSuffix(name, ".json") || strings.HasPrefix(strings.TrimSpace(string(b)), "{") || strings.HasPrefix(strings.TrimSpace(string(b)), "[") {
		if err := json.Unmarshal(b, &generic); err != nil {
			return nil, usage("%s: %v", name, err)
		}
	} else {
		var y any
		if err := yaml.Unmarshal(b, &y); err != nil {
			return nil, usage("%s: %v", name, err)
		}
		generic = yamlToJSON(y)
	}
	switch v := generic.(type) {
	case map[string]any:
		for _, k := range []string{"tasks", "items"} {
			if list, ok := v[k].([]any); ok {
				generic = list
				break
			}
		}
	}
	jb, err := json.Marshal(generic)
	if err != nil {
		return nil, usage("%s: %v", name, err)
	}
	var items []planItem
	if _, isList := generic.([]any); isList {
		if err := json.Unmarshal(jb, &items); err != nil {
			return nil, usage("%s: %v", name, err)
		}
	} else {
		var one planItem
		if err := json.Unmarshal(jb, &one); err != nil {
			return nil, usage("%s: %v", name, err)
		}
		items = []planItem{one}
	}
	if len(items) == 0 {
		return nil, usage("%s holds no tasks", name)
	}
	return items, nil
}

// yamlToJSON converts yaml.v3's map[string]any/[]any tree (which may
// contain map[any]any for non-string keys) into JSON-encodable values.
func yamlToJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = yamlToJSON(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = yamlToJSON(val)
		}
		return out
	case []any:
		for i := range t {
			t[i] = yamlToJSON(t[i])
		}
		return t
	}
	return v
}

func (it planItem) change(i int) (plan.Change, error) {
	if strings.TrimSpace(it.Title) == "" {
		return nil, usage("item %d: title is required", i+1)
	}
	if it.Kind == "group" {
		if len(it.Checks)+len(it.OptionalChecks)+len(it.Requires)+len(it.Blocks)+len(it.Acceptance)+len(it.Constraints)+len(it.Pins) > 0 || it.Cohort != "" || it.Outcome != "" || it.Size != "" {
			return nil, usage("item %d (%s): a group takes only key, title and parent", i+1, it.Title)
		}
		return plan.AddGroup{Key: it.Key, Title: it.Title, Parent: plan.Ref(it.Parent)}, nil
	}
	if it.Kind != "" && it.Kind != "task" {
		return nil, usage("item %d: kind must be task or group", i+1)
	}
	t := plan.AddTask{Key: it.Key, Title: it.Title, Outcome: it.Outcome, Parent: plan.Ref(it.Parent), Constraints: it.Constraints, Acceptance: it.Acceptance, Requires: refs(it.Requires), Blocks: refs(it.Blocks), Cohort: it.Cohort, Pins: it.Pins, Size: it.Size}
	for n, c := range it.Checks {
		cs, err := c.spec(n+1, true)
		if err != nil {
			return nil, err
		}
		t.TaskChecks = append(t.TaskChecks, cs)
	}
	for n, c := range it.OptionalChecks {
		cs, err := c.spec(len(it.Checks)+n+1, false)
		if err != nil {
			return nil, err
		}
		t.TaskChecks = append(t.TaskChecks, cs)
	}
	return t, nil
}

// openPlanFile opens a plan file path, or stdin for "-".
func openPlanFile(path string, stdin io.Reader) (string, io.ReadCloser, error) {
	if path == "-" {
		return "stdin", io.NopCloser(stdin), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", nil, usage("%v", err)
	}
	return path, f, nil
}
