#!/usr/bin/env sh
# Claude Code as the worker. Widen --allowedTools for the repository's own
# tools, or run in a sandbox with --dangerously-skip-permissions.
BOOTSTRAP='You are a coding agent responsible for one task in an isolated Git worktree. Use the shell and the atask CLI to execute and manage the task; atask is authoritative for task state, verification and completion. ATASK_OUTPUT=json is set: every atask command returns one JSON envelope; inspect error.details when one fails. Follow the project instructions (AGENTS.md). Commit before atask verify complete; only a passing verification completes the task. If any atask command returns INVALID_SESSION, stop editing immediately.'
run_agent() {
	claude -p --output-format text --permission-mode acceptEdits \
		--append-system-prompt "$BOOTSTRAP" \
		--allowedTools "Bash(atask *)" "Bash(git *)" "Bash(wt *)" "Bash(go *)" "Bash(npm *)" "Bash(make *)" "${CLAUDE_ARGS:-}"
}
# shellcheck source=common.sh
. "$(dirname "$0")/common.sh"
