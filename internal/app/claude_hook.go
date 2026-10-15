package app

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

const (
	// ContractCommand prints the static operating contract.
	ContractCommand = "atask context"
	// CurrentTaskCommand prints the current claim's context.
	CurrentTaskCommand = "atask context --hook"
	// ChangedTaskCommand prints it only when it changed since the last prompt.
	ChangedTaskCommand = "atask context --hook --changed"
)

const claudeSettingsPath = ".claude/settings.json"

// ClaudeHook is one managed hook entry: the event it fires on, an optional
// matcher, and the command it runs.
type ClaudeHook struct {
	Event, Matcher, Command string
}

// ManagedClaudeHooks are the hooks init installs: the contract once at
// startup, the current task after compaction or resume, and the current task
// again on a prompt only when it changed.
var ManagedClaudeHooks = []ClaudeHook{
	{"SessionStart", "startup", ContractCommand},
	{"SessionStart", "compact|resume", CurrentTaskCommand},
	{"UserPromptSubmit", "", ChangedTaskCommand},
}

// legacyHookCommands are commands earlier versions installed; init replaces them.
var legacyHookCommands = []string{"atask context && atask context --hook", ContractCommand}

// installClaudeHook ensures <root>/.claude/settings.json holds the managed
// hooks, keeping all other settings and replacing the legacy single hook.
// Idempotent.
func installClaudeHook(root string) error {
	settings := map[string]any{}
	raw, err := readUnder(root, claudeSettingsPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return fault.WrapInternal(err, "read "+claudeSettingsPath)
	default:
		if err := json.Unmarshal(raw, &settings); err != nil {
			return fault.WrapInternal(err, "parse "+claudeSettingsPath)
		}
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	managed := append([]string{CurrentTaskCommand, ChangedTaskCommand}, legacyHookCommands...)
	for _, h := range ManagedClaudeHooks {
		entries, _ := hooks[h.Event].([]any)
		hooks[h.Event] = withoutCommands(entries, managed)
	}
	for _, h := range ManagedClaudeHooks {
		entries, _ := hooks[h.Event].([]any)
		entry := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": h.Command}}}
		if h.Matcher != "" {
			entry["matcher"] = h.Matcher
		}
		hooks[h.Event] = append(entries, entry)
	}
	settings["hooks"] = hooks
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fault.WrapInternal(err, "encode "+claudeSettingsPath)
	}
	if err := mkdirUnder(root, ".claude"); err != nil {
		return fault.WrapInternal(err, "create .claude")
	}
	if err := writeUnder(root, claudeSettingsPath, append(out, '\n')); err != nil {
		return fault.WrapInternal(err, "write "+claudeSettingsPath)
	}
	return nil
}

// withoutCommands drops entries whose hooks run one of the given commands.
func withoutCommands(entries []any, commands []string) []any {
	kept := []any{}
	for _, e := range entries {
		m, _ := e.(map[string]any)
		hs, _ := m["hooks"].([]any)
		drop := false
		for _, h := range hs {
			hm, _ := h.(map[string]any)
			for _, c := range commands {
				drop = drop || hm["command"] == c
			}
		}
		if !drop {
			kept = append(kept, e)
		}
	}
	return kept
}

func mkdirUnder(root, name string) error {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Mkdir(name, 0o750); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}
