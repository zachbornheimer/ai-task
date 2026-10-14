package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zachbornheimer/ai-task/internal/plan"
)

// planItem is one task or group in a plan file. The field names are the
// ones `atask show` prints, so a planner can read the plan out and write it
// back in the same vocabulary.
type planItem struct {
	Kind        string   `json:"kind"` // task (default) | group
	Key         string   `json:"key"`
	Title       string   `json:"title"`
	Outcome     string   `json:"outcome"`
	Parent      string   `json:"parent"`
	Constraints []string `json:"constraints"`
	Acceptance  []string `json:"acceptance"`
	Requires    []string `json:"requires"`
	Blocks      []string `json:"blocks"`
	Proof       string   `json:"proof"`
	Regression  string   `json:"regression"`
	Size        string   `json:"size"`
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
	if v, ok := generic.(map[string]any); ok {
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
	flags := proofFlags{proof: it.Proof, regression: it.Regression}
	proof, regression, err := flags.parse(it.Size)
	if err != nil {
		return nil, usage("item %d (%s): %v", i+1, it.Title, err)
	}
	if it.Kind == "group" {
		if len(it.Requires)+len(it.Blocks)+len(it.Acceptance)+len(it.Constraints) > 0 || it.Outcome != "" {
			return nil, usage("item %d (%s): a group takes key, title, parent, proof, regression and size", i+1, it.Title)
		}
		return plan.AddGroup{Key: it.Key, Title: it.Title, Parent: plan.Ref(it.Parent), Size: it.Size, Proof: proof, Regression: regression}, nil
	}
	if it.Kind != "" && it.Kind != "task" {
		return nil, usage("item %d: kind must be task or group", i+1)
	}
	return plan.AddTask{Key: it.Key, Title: it.Title, Outcome: it.Outcome, Parent: plan.Ref(it.Parent), Constraints: it.Constraints, Acceptance: it.Acceptance, Requires: refs(it.Requires), Blocks: refs(it.Blocks), Size: it.Size, Proof: proof, Regression: regression}, nil
}

// openPlanFile opens a plan file path, or stdin for "-".
func openPlanFile(path string, stdin io.Reader) (string, io.ReadCloser, error) {
	if path == "-" {
		return "stdin", io.NopCloser(stdin), nil
	}
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", nil, usage("%v", err)
	}
	return path, f, nil
}
