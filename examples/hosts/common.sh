#!/usr/bin/env sh
# Shared loop for the per-harness host scripts: claim, hand the task
# context to the agent command on stdin inside the workspace, release
# with a note if the agent exits without completing. Several copies run
# in parallel. Requires: at on PATH, AT_DB/AT_PROJECT as for any at
# command, and run_agent() defined by the caller (reads the prompt on
# stdin, runs in the current directory, exits non-zero on failure).
set -eu
export AT_OUTPUT=json
json() { python3 -c 'import sys,json;d=json.load(sys.stdin);print(eval(sys.argv[1]))' "$1"; }

while :; do
  if ! session=$(at claim --wait); then
    code=$(printf '%s' "$session" | json 'd["error"]["code"]')
    case "$code" in
      DONE) echo "done: every task is complete"; exit 0 ;;
      STALLED) echo "stalled: a planner is needed" >&2; printf '%s\n' "$session" >&2; exit 2 ;;
      *) echo "claim failed: $session" >&2; exit 1 ;;
    esac
  fi
  task=$(printf '%s' "$session" | json 'd["result"]["task"]["id"]')
  workspace=$(printf '%s' "$session" | json 'd["result"]["workspace"]')
  echo "== $task in $workspace"
  (
    cd "$workspace"
    at context --output text | run_agent
  ) || true
  status=$(at show "$task" | json 'd["result"]["status"]')
  if [ "$status" = "complete" ]; then
    failures=0
    continue
  fi
  (cd "$workspace" && at claim release --note "agent exited without completing (status $status)") || true
  # An agent that keeps exiting without completing must not be re-run in
  # a hot loop: back off, and stop after a run of failures so a person
  # can look (the handoff carries the note).
  failures=$((${failures:-0} + 1))
  if [ "$failures" -ge "${MAX_AGENT_FAILURES:-5}" ]; then
    echo "stopping: $failures agent runs in a row ended without completing a task" >&2
    exit 3
  fi
  sleep $((failures * 10))
done
