# ADR 0017: One worktree per task, worktree-resident tokens, mandatory checks

**Status:** accepted in part (supersedes the per-attempt branches of ADR 0016; refines ADR 0006 and ADR 0010). The worktree-resident token and the post-commit hook were withdrawn by ADR 0018: a token readable from the worktree let a stale process borrow its successor's authority.

**Decision.**

- Every project is a Git repository. `at init` runs `git init` (with an
  initial commit) when the directory is not one. Non-Git projects are gone.
- Every task has one worktree on branch `at/<task-id>`, created from the
  target branch at the first claim and reused by every later attempt. `at`
  never deletes it; worktree tooling prunes it.
- The claim stores the session token in the worktree's private Git
  directory (`.git/worktrees/<name>/at-session`). Commands run inside the
  worktree find it without `AT_SESSION`; explicit sources still win.
- Leases default to 30 minutes (5 minutes to 4 hours). Every authenticated
  command renews the lease, and `init` installs a post-commit hook that
  runs `at claim renew`, so committing is renewing.
- Checks are mandatory: `add` refuses a task without task checks, `update`
  cannot empty them, and `claim` refuses while the project has no
  regression checks (`MISSING_VERIFICATION`). A trivial check is the
  planner's own risk.
- The claim handoff includes learnings from direct prerequisites and the
  failed checks (with output tails) of the newest failed run.
- Archiving records a reason. JSON is compact by default.

**Rationale.** Agents lose tokens and forget to renew; a token that lives
where the agent works and a lease that follows its commits remove both
failure modes without a side channel. One branch per task is what humans
and worktree tools expect (`at/<id>`), and reusing it makes a retry start
where the last attempt stopped instead of re-deriving it. A task that can
never complete should never be planned or handed out, so the checks that
completion depends on are required up front. A new attempt that does not
know why the last one failed repeats it.

**Consequences.** Uncommitted edits from a stale attempt are visible to
the next one (`workspace_dirty`); the token is readable by the same OS
user, like the database; a lost token with no worktree leaves the task
claimed until the lease expires.
