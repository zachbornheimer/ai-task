#!/usr/bin/env sh
# Prove host loops call atask (not POSIX /usr/bin/at) and exit 0 on an
# empty plan. Uses a throwaway git repo + ATASK_DB so a live in-progress
# task in another project cannot block claim --wait.
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
d=$(mktemp -d "${TMPDIR:-/tmp}/atask-empty-host.XXXXXX")
cleanup() { rm -rf "$d"; }
trap cleanup EXIT
git -C "$d" init -q
ATASK_DB="$d/db" atask init --path "$d" --name empty-host --no-instructions --db "$d/db" >/dev/null
ATASK_DB="$d/db" atask project --db "$d/db" --project empty-host --regression-check "true: true" >/dev/null
unset ATASK_SESSION
export ATASK_DB="$d/db" ATASK_PROJECT=empty-host
TIMEOUT="timeout"
if timeout --foreground 1 true >/dev/null 2>&1; then
	TIMEOUT="timeout --foreground"
fi
run() {
	# shellcheck disable=SC2086
	$TIMEOUT 10 "$@"
}
cd "$root"
run sh examples/hosts/codex.sh
run sh examples/hosts/opencode.sh
run sh examples/hosts/claude-code.sh
AGENT_CMD='cat >/dev/null' run sh examples/hosts/custom.sh
AGENT_CMD='cat >/dev/null' run sh examples/executor.sh
