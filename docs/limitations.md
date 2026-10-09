# Explicit limitations and unsolved risks

1. **Stale processes and files.** A lease proves authority over the task
   record, and per-task worktrees keep tasks' files apart, but the engine
   cannot stop an expired agent process from editing the worktree that the
   next attempt reuses. The host must cancel the agent when `Renew` fails;
   the next claim reports `workspace_dirty` when uncommitted edits are
   present. Final checks run on a detached snapshot, so a stale editor
   cannot change what is verified.

2. **Local-user trust.** `--planner` and `AT_SESSION` are conventions. The
   same OS user can edit the SQLite file, the repository, or the checks.
   Self-certification is impossible *through the API*; it is not a defence
   against a hostile local user. That needs a protected service or CI.

3. **Checks are declared, not proven, side-effect free.** They run in a
   disposable snapshot with `GOFLAGS=-count=1` and without the session
   token, but nothing stops a check from touching shared services. Task
   and regression suites run sequentially because isolation cannot be
   proven; a declared-isolation flag is a future addition.

4. **Semantic verification is imperfect.** A passing check proves an exit
   status on a revision. Acceptance criteria are for reviewers.

5. **Human gates are unsupported.** A task whose outcome only a human can
   produce still needs a runnable check to complete. Model the human's
   artefact (a file, a setting) as the check target.

6. **Regression policy edits apply to future runs only.** Completed tasks
   are not re-judged when the project's regression list changes; task
   contracts on completed tasks cannot change at all.

7. **Cohort blame is coarse.** A failing shared regression sends every
   member back; a failing member check sends that member back.

8. **Worktrees and branches are not pruned.** An agent's shell may still
   be inside one, and the worktree is the next attempt's starting point.
   Prune `at/<id>` worktrees and branches with your worktree tooling once
   the task is complete.

9. **Git only.** A project must be a repository (`at init` creates one).
   Evidence is bound to the verified revision; a trivial check (`true`)
   satisfies the mandatory-check rule and is the planner's own risk.

12. **Worktree-resident tokens** are readable by the same OS user, like
    the database. A lost token with no worktree leaves the task claimed
    until the lease (at most 4 hours) expires; that is accepted.

10. **Single host, local filesystem.** WAL-mode SQLite on a network
    filesystem is unsafe.

11. **`at` collides with POSIX `at`.** The name is provisional; install
    under another name if the scheduler is in use.
