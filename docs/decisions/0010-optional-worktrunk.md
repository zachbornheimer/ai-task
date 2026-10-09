# ADR 0010: Optional Worktrunk, no mandatory worktree coupling

**Status:** accepted (implementation in Milestone 3)

**Decision.** A small `Workspace` boundary (`Ensure`, `Inspect`, `Revision`)
with three implementations: Worktrunk adapter (`wt switch -c task-<id>`, `wt
list --format=json`), plain `git worktree`, and no-op. Branch names are task
IDs. `wt merge` is used only for real promotion, never as a dry run, because it
advances the target and removes the source worktree.

**Rationale.** Worktrunk already does worktree lifecycle and hooks well; tasks
that are not code need no workspace at all.
