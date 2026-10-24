# Explicit limitations and unsolved risks

1. **Stale processes and files.** A lease proves authority over the task
   record, and per-task worktrees keep tasks' files apart, but the engine
   cannot stop an expired agent process from editing the worktree that the
   next attempt reuses. The host must cancel the agent when `Renew` fails;
   the next claim reports `workspace_dirty` when uncommitted edits are
   present. Final proofs run on a detached snapshot, so a stale editor
   cannot change what is verified.

2. **Local-user trust.** Planner authority (no task token) and `ATASK_SESSION` are conventions. The
   same OS user can edit the SQLite file, the repository, or the proofs.
   Self-certification is impossible _through the API_; it is not a defence
   against a hostile local user. That needs a protected service or CI.

3. **Proofs are declared, not proven, side-effect free.** They run in a
   disposable snapshot with `GOFLAGS=-count=1` and without the session
   token, but nothing stops a check from touching shared services. The proof
   and regression run sequentially because isolation cannot be
   proven; a declared-isolation flag is a future addition.

4. **Semantic verification is imperfect.** A passing check proves an exit
   status on a revision. Acceptance criteria are for reviewers.

5. **Human gates are unsupported.** A task whose outcome only a human can
   produce still needs a runnable check to complete. Model the human's
   artefact (a file, a setting) as the check target.

6. **Completed tasks are not re-judged.** Task contracts on completed
   tasks cannot change at all.

7. **Branches are not pruned.** `atask prune` removes worktrees of complete
   and archived tasks and quarantined directories; `atask/<id>` branches
   stay for history and are yours to delete.

8. **Git only.** A project must be a repository (`atask init` creates one).
   Evidence is bound to the verified revision; a trivial proof
   satisfies the mandatory-proof rule and is the planner's own risk.

9. **Worktree-resident tokens and stale processes.** The token in a
   worktree's Git directory is readable by the same OS user, like the
   database. A stale process whose attempt expired is contained by
   quarantine: the next claim moves its worktree aside, detached, so it
   keeps only its own expired token and its commits never reach the
   branch. It still burns CPU and disk until the host stops it; its
   quarantined directory (`<path>.stale-<seq>`) is removed when the task
   completes or by `atask prune`, which is the operator's decision to stop
   caring about that process.
   A lost token leaves the task claimed until the lease (at most 30
   minutes) expires; that is accepted.

10. **Single host, local filesystem.** WAL-mode SQLite on a network
    filesystem is unsafe.

11. **Proofs are confined only on macOS, and rootless projects not at
    all.** Each proof runs on its own disposable worktree with a private
    `TMPDIR` and no session token, and repo-backed projects' checks
    additionally run under a Seatbelt profile that denies home writes and
    secret reads — but a proof can still reach the network and shared
    services. A rootless project (no repository, including `general`) runs
    its checks and actions entirely unconfined.

12. **`at` collides with POSIX `at`.** The name is provisional; install
    under another name if the scheduler is in use.
