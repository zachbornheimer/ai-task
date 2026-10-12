#!/usr/bin/env sh
# OpenCode as the worker: reads AGENTS.md itself; atask context ID is the prompt.
run_agent() { opencode run "$(cat)"; }
. "$(dirname "$0")/common.sh"
