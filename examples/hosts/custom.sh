#!/usr/bin/env sh
# Any agent: AGENT_CMD reads the task context (with the rules appended)
# on stdin, runs in the workspace, and must drive atask itself.
: "${AGENT_CMD:?set AGENT_CMD to the agent command that reads its prompt on stdin}"
run_agent() { atask context --with-rules --output text | sh -c "$AGENT_CMD"; }
. "$(dirname "$0")/common.sh"
