# ADR 0016: Per-attempt worktrees, detached snapshots, guarded promotion

**Status:** accepted (refines ADR 0010 and ADR 0012)

**Decision.** Each execution attempt gets a private Git worktree on
`at/<task>/<n>`, continuing the previous attempt's branch. Final checks run
in a detached snapshot of the submitted commit. Promotion is two-phase:
build a merge candidate on the current target in a scratch worktree, verify
it when it changed content, then compare-and-swap the target (fast-forward
a clean checked-out target in place, or `update-ref` with the expected old
value), rebuilding on a moved base up to three times. Git projects default
to integration policy `promote`.

**Rationale.** Database fencing cannot stop a stale process from editing
files; separate worktrees per attempt contain the damage. A snapshot
guarantees the evidence describes the commit, not whatever the agent typed
next. Compare-and-swap promotion means a failing candidate or a concurrent
promotion never moves the target.

**Consequences.** Worktrees accumulate until pruned; projects without Git
get no revision binding.
