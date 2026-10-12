#!/usr/bin/env sh
# Prove an unscoped worker receives ATASK_SESSION and can verify complete
# without a git worktree token stash.
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
d=$(mktemp -d "${TMPDIR:-/tmp}/atask-unscoped-host.XXXXXX")
cleanup() { rm -rf "$d"; }
trap cleanup EXIT
export ATASK_DB="$d/db"
unset ATASK_SESSION ATASK_PROJECT
mkdir -p "$d/bin"
cat >"$d/bin/grok" <<'EOF'
#!/bin/sh
if [ -z "${ATASK_SESSION:-}" ]; then
	echo "ATASK_SESSION missing" >&2
	exit 1
fi
atask log --note "unscoped host supplied the token" >/dev/null
atask verify complete >/dev/null
printf 'HARNESS_INVOKED grok\n'
exit 0
EOF
chmod +x "$d/bin/grok"
export PATH="$d/bin:$PATH"
export MAX_AGENT_FAILURES=1
export ATASK_HEARTBEAT_SECS=30
cd "$d"
atask add "unscoped host work" --check "u: true" >/dev/null
out=$(sh "$root/examples/hosts/grok.sh" 2>&1 || true)
printf '%s\n' "$out"
printf '%s\n' "$out" | grep -q HARNESS_INVOKED || {
	echo "expected grok host to invoke the stub" >&2
	exit 1
}
printf '%s\n' "$out" | grep -q "done: every task is complete" || {
	echo "expected unscoped claim --wait to return DONE after completion" >&2
	exit 1
}
status=$(atask list complete --output json | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d["result"]["tasks"][0]["status"])')
[ "$status" = "complete" ]
