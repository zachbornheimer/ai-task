#!/usr/bin/env sh
# Codex CLI as the worker: reads AGENTS.md itself; the context is the prompt.
run_agent() { codex exec --full-auto "${CODEX_ARGS:-}" "$(cat)"; }
set -eu
export ATASK_OUTPUT=json
json() { python3 -c 'import sys,json;d=json.load(sys.stdin);print(eval(sys.argv[1]))' "$1"; }

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
	echo "== $task in $workspace"
	(
		cd "$workspace"
		atask show --output text | run_agent
	) || true
	status=$(atask show "$task" | json 'd["result"]["status"]')
	if [ "$status" = "complete" ]; then
		failures=0
		continue
	fi
	(cd "$workspace" && atask claim release --note "agent exited without completing (status $status)") || true
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
