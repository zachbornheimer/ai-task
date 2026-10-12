#!/usr/bin/env sh
# Prove host loops claim a live task and invoke the harness binary
# (stubbed), not POSIX /usr/bin/at. Throwaway git repo + ATASK_DB so a
# live in-progress task in another project cannot steal the claim.
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
d=$(mktemp -d "${TMPDIR:-/tmp}/atask-claimed-host.XXXXXX")
cleanup() {
	git -C "$d" worktree prune -q 2>/dev/null || true
	rm -rf "$d" "$(dirname "$d")/.worktrees/$(basename "$d")"-atask-at-*
}
trap cleanup EXIT
git -C "$d" init -q
ATASK_DB="$d/db" atask init --path "$d" --name claimed-host --no-instructions --db "$d/db" >/dev/null
ATASK_DB="$d/db" atask project --db "$d/db" --project claimed-host --regression-check "true: true" >/dev/null
ATASK_DB="$d/db" atask add --db "$d/db" --project claimed-host "stub task" --outcome "stub harness is invoked" --check "noop: true" >/dev/null
mkdir -p "$d/bin"
cat >"$d/bin/stub" <<'EOF'
#!/bin/sh
printf 'HARNESS_INVOKED %s\n' "${0##*/}"
cat >/dev/null || true
exit 0
EOF
chmod +x "$d/bin/stub"
for name in grok codex opencode claude; do
	ln -s stub "$d/bin/$name"
done
unset ATASK_SESSION
export ATASK_DB="$d/db" ATASK_PROJECT=claimed-host MAX_AGENT_FAILURES=1
export PATH="$d/bin:$PATH"
cd "$root"
invoke() {
	host=$1
	out=$(sh "examples/hosts/$host" 2>&1 || true)
	printf '%s\n' "$out"
	printf '%s\n' "$out" | grep -q HARNESS_INVOKED || {
		echo "expected $host to invoke the stub harness" >&2
		exit 1
	}
	printf '%s\n' "$out" | grep -q "== at-" || {
		echo "expected $host to claim a task" >&2
		exit 1
	}
}
invoke grok.sh
invoke codex.sh
invoke opencode.sh
invoke claude-code.sh
