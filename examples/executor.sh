#!/usr/bin/env sh
# Shell driver for machine-only execution of a task graph with the `at`
# CLI. Prefer examples/embedded_runner (Go) for a real host; this is the
# same loop in shell. Run several copies for parallel agents.
#
# Usage: AT_PROJECT=<id|name> AGENT_CMD='claude -p' examples/executor.sh
#   AGENT_CMD receives the session JSON on stdin with AT_SESSION exported
#   and must `at log`, commit, and `at verify complete` itself.
set -eu
export AT_OUTPUT=json
AGENT_CMD=${AGENT_CMD:?set AGENT_CMD to the agent CLI that reads a prompt on stdin}

while :; do
  if ! session=$(at claim --wait); then
    code=$(printf '%s' "$session" | jq -r .error.code)
    case "$code" in
      DONE) echo "done"; exit 0 ;;
      STALLED) echo "stalled: human action needed" >&2; printf '%s\n' "$session" | jq .error.details >&2; exit 2 ;;
      *) echo "claim failed: $session" >&2; exit 1 ;;
    esac
  fi
  AT_SESSION=$(printf '%s' "$session" | jq -r .result.token); export AT_SESSION
  task_id=$(printf '%s' "$session" | jq -r .result.task.id)
  workspace=$(printf '%s' "$session" | jq -r .result.workspace)
  echo "working on $task_id in $workspace"
  ( cd "$workspace" && printf '%s\n%s\n' \
    "Implement the claimed task's outcome and acceptance criteria. Use \`at log\` for progress, \`at verify task\`/\`at verify regression\` while iterating, commit, then \`at verify complete\`. If prerequisite work is needed: \`at add \"...\" --blocks $task_id\`, log, \`at claim release -\`. Stop editing if authorization fails." \
    "$session" | $AGENT_CMD ) || at claim release - --failed --note "agent exited with an error" <<EOF2 || true
$AT_SESSION
EOF2
done
