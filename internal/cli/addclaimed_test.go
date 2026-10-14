package cli

import (
	"encoding/json"
	"testing"
)

// addClaimed files a task with `atask add` and takes it with `atask claim`,
// returning the two results joined as {"result":{"task":..,"session":..}}.
func addClaimed(t *testing.T, run func(args ...string) (int, string, string), addArgs ...string) (int, string, string) {
	t.Helper()
	code, out, errOut := run(append([]string{"--json", "add"}, addArgs...)...)
	if code != ExitOK {
		return code, out, errOut
	}
	var added struct {
		Result struct {
			Task json.RawMessage `json:"task"`
		} `json:"result"`
	}
	var task struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil || json.Unmarshal(added.Result.Task, &task) != nil || task.ID == "" {
		t.Fatalf("add result has no task id: %v\n%s", err, out)
	}
	code, claimOut, errOut := run("--json", "claim", task.ID)
	if code != ExitOK {
		return code, claimOut, errOut
	}
	var claimed struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(claimOut), &claimed); err != nil {
		t.Fatalf("claim result: %v\n%s", err, claimOut)
	}
	joined, err := json.Marshal(map[string]any{"ok": true, "result": map[string]any{"task": added.Result.Task, "session": claimed.Result}})
	if err != nil {
		t.Fatal(err)
	}
	return ExitOK, string(joined), ""
}
