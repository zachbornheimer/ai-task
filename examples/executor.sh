#!/usr/bin/env sh
# Shell driver for machine-only execution of a task graph with the `atask`
# CLI. Prefer examples/embedded_runner (Go) for a real host; this is the
# same loop in shell. Run several copies for parallel agents.
#
# Usage: ATASK_PROJECT=<id|name> AGENT_CMD='claude -p' examples/executor.sh
#   AGENT_CMD receives the session JSON on stdin with ATASK_SESSION exported
#   and must `atask log`, commit, and `atask verify complete` itself.
set -eu
export ATASK_OUTPUT=json
AGENT_CMD=${AGENT_CMD:?set AGENT_CMD to the agent CLI that reads a prompt on stdin}

while :; do
	if ! session=$(atask claim --wait); then
		code=$(printf '%s' "$session" | jq -r .error.code)
		case "$code" in
		DONE)
			echo "done"
			exit 0
			;;
		STALLED)
			echo "stalled: human action needed" >&2
			printf '%s\n' "$session" | jq .error.details >&2
			exit 2
			;;
		*)
			echo "claim failed: $session" >&2
			exit 1
			;;
		esac
	fi
	ATASK_SESSION=$(printf '%s' "$session" | jq -r .result.token)
	export ATASK_SESSION
	task_id=$(printf '%s' "$session" | jq -r .result.task.id)
	workspace=$(printf '%s' "$session" | jq -r .result.workspace)
	echo "working on $task_id in $workspace"
	(cd "$workspace" && printf '%s\n%s\n' \
		"Implement the claimed task's outcome and acceptance criteria. Use \`atask log\` for progress, \`atask verify proof\`/\`atask verify regression\` while iterating, commit, then \`atask verify complete\`. If prerequisite work is needed: \`atask add \"...\" --blocks $task_id\`, log, \`atask claim release -\`. Stop editing if authorization fails." \
		"$session" | $AGENT_CMD) || atask claim release - --failed --note "agent exited with an error" <<EOF2 || true
$ATASK_SESSION
EOF2
done
