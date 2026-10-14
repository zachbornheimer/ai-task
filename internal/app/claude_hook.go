package app

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// SessionStartCommand is the one command the Claude Code hook runs. A
// SessionStart hook fires again after compaction, so no other hook is needed.
const SessionStartCommand = "atask context"

const claudeSettingsPath = ".claude/settings.json"

// installClaudeHook ensures <root>/.claude/settings.json holds a
// SessionStart hook that runs SessionStartCommand, keeping all other
// settings. Idempotent.
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
	entries, _ := hooks["SessionStart"].([]any)
	if hasSessionStartCommand(entries) {
		return nil
	}
	hooks["SessionStart"] = append(entries, map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": SessionStartCommand}},
	})
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

func hasSessionStartCommand(entries []any) bool {
	for _, e := range entries {
		m, _ := e.(map[string]any)
		hs, _ := m["hooks"].([]any)
		for _, h := range hs {
			if hm, _ := h.(map[string]any); hm["command"] == SessionStartCommand {
				return true
			}
		}
	}
	return false
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
