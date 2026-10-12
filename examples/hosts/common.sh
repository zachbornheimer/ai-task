#!/usr/bin/env sh
# Shared loop for the per-harness host scripts: claim, export the session
# token, hand the worker handoff (`atask context ID`) to the agent on stdin,
# heartbeat the lease, release if the agent exits without completing.
# Requires: atask on PATH, ATASK_DB/ATASK_PROJECT as for any atask command,
# and run_agent() defined by the caller (reads the prompt on stdin, runs in
# the current directory, exits non-zero on failure).
set -eu
export ATASK_OUTPUT=json
json() { python3 -c 'import sys,json;d=json.load(sys.stdin);print(eval(sys.argv[1]))' "$1"; }

heartbeat() {
	pid=$1
	secs=${ATASK_HEARTBEAT_SECS:-60}
	while :; do
		sleep "$secs"
		if ! atask claim renew >/dev/null 2>&1; then
			kill "$pid" 2>/dev/null || true
			wait "$pid" 2>/dev/null || true
			echo "stopping: lease renewal failed; worker cancelled" >&2
			exit 4
		fi
	done
}

while :; do
	if ! session=$(atask claim --wait); then
		code=$(printf '%s' "$session" | json 'd["error"]["code"]')
		case "$code" in
		DONE)
			echo "done: every task is complete"
			exit 0
			;;
		STALLED)
			echo "stalled: a planner is needed" >&2
			printf '%s\n' "$session" >&2
			exit 2
			;;
		*)
			echo "claim failed: $session" >&2
			exit 1
			;;
		esac
	fi
	task=$(printf '%s' "$session" | json 'd["result"]["task"]["id"]')
	workspace=$(printf '%s' "$session" | json 'd["result"]["workspace"]')
	token=$(printf '%s' "$session" | json 'd["result"]["token"]')
	export ATASK_SESSION="$token"
	if [ -z "$workspace" ] || [ "$workspace" = "None" ] || [ "$workspace" = "null" ]; then
		workspace=$PWD
	fi
	echo "== $task in $workspace"
	(
		cd "$workspace"
		atask context "$task" --output text | run_agent
	) &
	agent_pid=$!
	heartbeat "$agent_pid" &
	hb_pid=$!
	wait "$agent_pid" || true
	kill "$hb_pid" 2>/dev/null || true
	wait "$hb_pid" 2>/dev/null || true
	status=$(atask show "$task" | json 'd["result"]["status"]')
	if [ "$status" = "complete" ]; then
		failures=0
		continue
	fi
	(cd "$workspace" && atask claim release --note "agent exited without completing (status $status)") || true
	failures=$((${failures:-0} + 1))
	if [ "$failures" -ge "${MAX_AGENT_FAILURES:-5}" ]; then
		echo "stopping: $failures agent runs in a row ended without completing a task" >&2
		exit 3
	fi
	sleep $((failures * 10))
done
