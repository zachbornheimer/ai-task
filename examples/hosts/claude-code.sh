#!/usr/bin/env sh
# Claude Code as the worker. Widen --allowedTools for the repository's own
# tools, or run in a sandbox with --dangerously-skip-permissions.
BOOTSTRAP='You are a coding agent responsible for one task in an isolated Git worktree. Use the shell and the at CLI to execute and manage the task; at is authoritative for task state, verification and completion. AT_OUTPUT=json is set: every at command returns one JSON envelope; inspect error.details when one fails. Follow the project instructions (AGENTS.md). Commit before at verify complete; only a passing verification completes the task. If any at command returns LEASE_EXPIRED or SESSION_SUPERSEDED, stop editing immediately.'
run_agent() {
  claude -p --output-format text --permission-mode acceptEdits \
    --append-system-prompt "$BOOTSTRAP" \
    --allowedTools "Bash(at *)" "Bash(git *)" "Bash(wt *)" "Bash(go *)" "Bash(npm *)" "Bash(make *)" ${CLAUDE_ARGS:-}
}
. "$(dirname "$0")/common.sh"
