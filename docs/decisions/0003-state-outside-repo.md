# ADR 0003: State outside the repository

**Status:** accepted

**Context.** Beads keeps `.beads/` inside the repository so state travels with
Git. Our tasks span worktrees of one repository, and a worktree is created per
task.

**Decision.** One database per user in the OS state directory
(`$XDG_STATE_HOME/tasks/tasks.db`, `~/Library/Application Support/tasks/`,
`%LOCALAPPDATA%\tasks\`), overridable with `TASKS_DB`. Projects map to
repositories by registered root path; any linked worktree resolves to its main
worktree without invoking Git.

**Rationale.** A per-worktree `.beads`-style directory would fragment state
across task worktrees or require ignoring and syncing it; committing task state
into the repository would put leases and tokens' digests into Git history.

**Consequences.** State does not travel with the repository; a future project
service can own sharing.
