#!/usr/bin/env sh
# Any agent: AGENT_CMD reads the worker handoff (`atask context ID`) on stdin,
# runs in the workspace, and must drive atask itself.
: "${AGENT_CMD:?set AGENT_CMD to the agent command that reads its prompt on stdin}"
run_agent() { sh -c "$AGENT_CMD"; }
. "$(dirname "$0")/common.sh"
