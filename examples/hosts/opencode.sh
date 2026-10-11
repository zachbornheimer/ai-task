#!/usr/bin/env sh
# OpenCode as the worker: reads AGENTS.md itself; the context is the prompt.
run_agent() { opencode run "${OPENCODE_ARGS:-}" "$(cat)"; }
# shellcheck source=common.sh
. "$(dirname "$0")/common.sh"
