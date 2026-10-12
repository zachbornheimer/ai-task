#!/usr/bin/env sh
# Codex CLI as the worker: reads AGENTS.md itself; atask context ID is the prompt.
run_agent() { codex exec --full-auto "$(cat)"; }
. "$(dirname "$0")/common.sh"
