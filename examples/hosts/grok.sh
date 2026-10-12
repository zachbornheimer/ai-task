#!/usr/bin/env sh
# Grok as the worker: reads AGENTS.md itself; atask context ID is the prompt.
# Optional model: GROK_MODEL.
run_agent() {
	if [ -n "${GROK_MODEL:-}" ]; then
		grok --always-approve --output-format plain --no-auto-update -m "$GROK_MODEL" -p "$(cat)"
	else
		grok --always-approve --output-format plain --no-auto-update -p "$(cat)"
	fi
}
. "$(dirname "$0")/common.sh"
