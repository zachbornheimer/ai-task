package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// Markers delimit the block `at init` owns inside AGENTS.md. Text outside
// the markers belongs to the repository and is never touched.
const (
	InstructionsBegin = "<!-- at:begin -->"
	InstructionsEnd   = "<!-- at:end -->"
)

// Instructions is the project instruction block for coding agents: how
// work gets done through `at` (any request becomes tasks), the agent
// verbs, and the execution contract. `at init` installs it into
// AGENTS.md between the markers and keeps it current on re-runs.
func Instructions() string {
	return `# Working with ` + "`at`" + ` (for coding agents)

` + "`at`" + ` is the task engine for this repository: it hands you one claimed task
at a time, remembers what the previous attempt learned, and decides
completion from fresh verification, never from your word. It works the
same from Claude Code, Codex CLI, OpenCode, Grok, or a shell. Every ` + "`at`" + `
command prints one JSON envelope when ` + "`AT_OUTPUT=json`" + ` is set; read
` + "`error.details`" + ` when one fails.

## How work gets done here

Everything that changes this repository goes through ` + "`at`" + `.

- A question, an explanation, or a review is not a task: answer it.
- Anything else starts with the plan: ` + "`at context`" + ` (or ` + "`at status`" + ` and
  ` + "`at list`" + `). If a task already covers the request, claim it. Otherwise
  add tasks, each with a check that proves it, then claim one:
  ` + "`at add \"title\" --check \"id: cmd\"`" + `, several at once with
  ` + "`at add -f plan.yaml`" + `, and ` + "`at add \"title\" --check \"...\" --claim`" + ` for
  one small change. Run ` + "`at doctor`" + ` before planning in a repository you
  have not worked in.
- Never edit the target branch directly; a passing ` + "`at verify complete`" + `
  promotes verified work there.
- One task = one independently verifiable outcome. Plan the checks before
  the code; the regression suite carries the weight, task checks prove
  the outcome, and ` + "`--pin`" + ` protects files the task must not touch.
- Checks must be fast. A task is ` + "`--size small`" + ` (30s of checks) unless
  declared medium (5m) or large (15m); the regression gate has 2m. A check
  that runs out of time is a verification-construction problem, not a
  code failure: prove only this outcome (` + "`-run`" + `, one package), split the
  task, or declare the size. Regression checks get ` + "`AT_CHANGED_FILES`" + ` to
  target affected tests; the full suite belongs in CI.

## Verbs

` + "```" + `
at context                                           your task: outcome, checks, workspace, state, handoff (or the plan, with no task in hand)
at claim [REF] [--wait]                              one leased task, its worktree on at/<id>; the token is stored there
at log --done "..." --next "..." --learned "..."     record progress; next is what the next attempt reads first
at verify task                                       run this task's checks (diagnostic, repeatable)
at verify regression                                 run the project's regression checks (diagnostic)
at verify complete                                   commit first; runs BOTH suites fresh and completes only if all pass
at add "prereq" --blocks <task> --check "id: cmd"    discovered prerequisite: then log and release
at claim release                                     hand the task back; keeps your handoff
at claim renew                                       extend the lease (every command does too)
` + "```" + `

` + Rules
}

// DefaultGuidelines is the repository section `at init` writes into a
// new AGENTS.md outside the markers: engineering rules the repository's
// owners are expected to edit.
const DefaultGuidelines = `# Engineering guidelines

Edit this section for the repository; the block above is owned by ` + "`at init`" + `.

- Prefer the smallest complete change that satisfies the task; follow the
  existing architecture and conventions; avoid unrelated refactoring,
  formatting, or dependency changes.
- Handle errors explicitly; preserve existing behaviour and public
  contracts unless the task requires otherwise.
- Test observable behaviour, including failure paths, with deterministic
  tests; fix defects rather than weakening assertions.
- Read the code and documentation the task needs, not the repository by
  default; keep documentation consistent when a documented contract changes.
- Report results and blockers concisely; distinguish verified results from
  assumptions.
`

// InstallResult says what InstallInstructions wrote.
type InstallResult struct {
	AgentsMD string `json:"agents_md"`           // path written or updated
	ClaudeMD string `json:"claude_md,omitempty"` // path written, if created or amended
	Created  bool   `json:"created"`             // AGENTS.md did not exist
	Updated  bool   `json:"updated"`             // the block changed
}

// InstallInstructions writes the instruction block into <root>/AGENTS.md
// between the markers (creating the file with DefaultGuidelines when it
// is absent, appending the block when the file exists without markers,
// replacing the block when it does) and makes sure CLAUDE.md imports
// AGENTS.md for harnesses that read only CLAUDE.md. Idempotent.
func InstallInstructions(root string) (InstallResult, error) {
	res := InstallResult{AgentsMD: filepath.Join(root, "AGENTS.md")}
	block := InstructionsBegin + "\n" + Instructions() + InstructionsEnd + "\n"
	existing, err := os.ReadFile(res.AgentsMD)
	var content string
	switch {
	case os.IsNotExist(err):
		content = block + "\n" + DefaultGuidelines
		res.Created, res.Updated = true, true
	case err != nil:
		return res, fault.Wrap(err, fault.CodeInternal, "read AGENTS.md")
	default:
		s := string(existing)
		b, e := strings.Index(s, InstructionsBegin), strings.Index(s, InstructionsEnd)
		if b >= 0 && e > b {
			content = s[:b] + block + strings.TrimLeft(s[e+len(InstructionsEnd):], "\n")
			if strings.TrimSpace(content) == strings.TrimSpace(s) {
				content = s
			} else {
				res.Updated = true
			}
		} else {
			content = strings.TrimRight(s, "\n") + "\n\n" + block
			res.Updated = true
		}
	}
	if res.Updated {
		if err := os.WriteFile(res.AgentsMD, []byte(content), 0o644); err != nil {
			return res, fault.Wrap(err, fault.CodeInternal, "write AGENTS.md")
		}
	}
	claude := filepath.Join(root, "CLAUDE.md")
	cb, err := os.ReadFile(claude)
	switch {
	case os.IsNotExist(err):
		if err := os.WriteFile(claude, []byte("@AGENTS.md\n"), 0o644); err != nil {
			return res, fault.Wrap(err, fault.CodeInternal, "write CLAUDE.md")
		}
		res.ClaudeMD = claude
	case err != nil:
		return res, fault.Wrap(err, fault.CodeInternal, "read CLAUDE.md")
	case !strings.Contains(string(cb), "@AGENTS.md"):
		if err := os.WriteFile(claude, []byte(strings.TrimRight(string(cb), "\n")+"\n\n@AGENTS.md\n"), 0o644); err != nil {
			return res, fault.Wrap(err, fault.CodeInternal, "amend CLAUDE.md")
		}
		res.ClaudeMD = claude
	}
	return res, nil
}

// ClaudeHooks are the Claude Code hooks `at init --claude` installs: the
// plan or task context at session start, a guard that keeps edits inside
// task worktrees, and a stop that refuses to end a turn with a claimed
// task neither verified nor released. Each one calls `at hook <event>`,
// which reads the hook's JSON on stdin and answers in Claude Code's
// format, so the project settings carry no shell logic.
var ClaudeHooks = map[string][]map[string]any{
	"SessionStart": {{"hooks": []map[string]any{{"type": "command", "command": "at hook session-start", "timeout": 30}}}},
	"PreToolUse":   {{"matcher": "Edit|Write|NotebookEdit", "hooks": []map[string]any{{"type": "command", "command": "at hook guard-edit", "timeout": 15}}}},
	"Stop":         {{"hooks": []map[string]any{{"type": "command", "command": "at hook stop", "timeout": 15}}}},
}

// InstallClaudeHooks merges ClaudeHooks into <root>/.claude/settings.json,
// keeping every other setting and any hook entries that are not ours.
// Returns the settings path and whether it changed.
func InstallClaudeHooks(root string) (string, bool, error) {
	dir := filepath.Join(root, ".claude")
	path := filepath.Join(dir, "settings.json")
	settings := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &settings); err != nil {
			return path, false, fault.Wrap(err, fault.CodeInvalidInput, "%s is not valid JSON", path)
		}
	} else if !os.IsNotExist(err) {
		return path, false, fault.Wrap(err, fault.CodeInternal, "read %s", path)
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	changed := false
	for event, ours := range ClaudeHooks {
		list, _ := hooks[event].([]any)
		for _, entry := range ours {
			cmd := entry["hooks"].([]map[string]any)[0]["command"].(string)
			if hasHookCommand(list, cmd) {
				continue
			}
			list = append(list, toAny(entry))
			changed = true
		}
		hooks[event] = list
	}
	settings["hooks"] = hooks
	if !changed {
		return path, false, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return path, false, fault.Wrap(err, fault.CodeInternal, "create %s", dir)
	}
	b, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return path, false, fault.Wrap(err, fault.CodeInternal, "write %s", path)
	}
	return path, true, nil
}

func hasHookCommand(list []any, cmd string) bool {
	for _, item := range list {
		entry, _ := item.(map[string]any)
		hs, _ := entry["hooks"].([]any)
		for _, h := range hs {
			hm, _ := h.(map[string]any)
			if fmt.Sprint(hm["command"]) == cmd {
				return true
			}
		}
	}
	return false
}

// toAny round-trips a typed map through JSON so it merges with values
// decoded from the settings file.
func toAny(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}
