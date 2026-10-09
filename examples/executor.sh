#!/usr/bin/env sh
# Reference driver for machine-only execution of a task graph.
#
# Loops: take the next takeable task (waiting while others are in flight),
# hand its session to a coding agent, and let the agent log/finish through
# the same CLI. Stops when the project is done or stuck. Run several copies
# for parallel agents; the engine serialises claims.
#
# Usage: TASKS_PROJECT=<id|name> AGENT_CMD='claude -p' examples/executor.sh
#   AGENT_CMD receives the session JSON on stdin and must run `tasks log`
#   and `tasks finish "$TASKS_SESSION"` itself (TASKS_SESSION is exported).
#   Examples: AGENT_CMD='claude -p'  |  AGENT_CMD='codex exec -'  |  AGENT_CMD='opencode run'
set -eu
export TASKS_OUTPUT=json
AGENT_CMD=${AGENT_CMD:?set AGENT_CMD to the agent CLI that reads a prompt on stdin}
WAIT=${WAIT:-10m}

while :; do
  status=$(tasks status)
  if [ "$(printf '%s' "$status" | jq -r .result.done)" = true ]; then echo "done"; exit 0; fi
  if [ "$(printf '%s' "$status" | jq -r .result.stuck)" = true ]; then
    echo "stuck: a human must act (manual tasks or unsatisfiable graph)" >&2; exit 2
  fi
  if ! session=$(tasks take --wait "$WAIT"); then
    code=$(printf '%s' "$session" | jq -r .error.code)
    [ "$code" = NO_AVAILABLE_TASK ] && continue   # others still in flight; re-check status
    echo "take failed: $session" >&2; exit 1
  fi
  export TASKS_SESSION=$(printf '%s' "$session" | jq -r .result.token)
  task_id=$(printf '%s' "$session" | jq -r .result.task_id)
  echo "working on $task_id"
  # The prompt is the session itself: contract, prerequisites, handoff.
  printf '%s\n%s\n' \
    "You are executing one task from a dependency graph. Use the tasks CLI (TASKS_SESSION is set): log progress with 'tasks log', add discovered prerequisites with 'tasks add \"...\" --blocks $task_id' then 'tasks release' to yield, and submit with 'tasks finish' after committing. Task session follows." \
    "$session" | $AGENT_CMD || true
  # Whatever the agent did, the engine's facts decide: finish, release, or
  # an expired lease leave a recoverable state for the next loop.
done
