#!/usr/bin/env sh
# Codex CLI as the worker: reads AGENTS.md itself; the context is the prompt.
run_agent() { codex exec --full-auto ${CODEX_ARGS:-} "$(cat)"; }
. "$(dirname "$0")/common.sh"
