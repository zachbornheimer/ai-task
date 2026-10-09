# Explicit limitations and unsolved risks

These are not planned to be hidden behind better wording. Each is a real gap.

1. **Filesystem ownership after lease expiry.** A lease proves authority
   over the task record. It does not stop an expired-but-alive process from
   continuing to edit files in the task's worktree while a new attempt also
   edits them. The engine cannot kill processes. Mitigations are external:
   process supervision, per-attempt worktrees (not in V1), or a long enough
   lease plus renewals. The agent contract tells a superseded agent to stop
   writing files; that is a convention, not enforcement.

2. **Local-user trust boundary.** The same OS user that runs agents can open
   the SQLite file, edit tests, or alter a policy. The engine makes
   self-certification impossible *through its API* (claims never complete
   tasks; policies are not reachable through session tokens); it does not
   defend against a hostile local user. A protected service or CI runner is
   needed for that and is out of scope for V1.

3. **Semantic verification is imperfect.** A passing check proves an exit
   status on a revision, not that the outcome is achieved. Acceptance
   criteria are for human review. The tool warns about multi-outcome tasks
   heuristically and never judges semantics with a model.

4. **A task without checks is complete on submission** under integration
   policy `none`. That is the honest meaning of an empty contract; choose it
   consciously, and attach at least one check to anything that matters.

5. **Verification runs in the project directory, not a detached checkout.**
   The executor refuses to run unless the tree is clean and at the submitted
   revision, which binds evidence to the revision, but between the check
   and the command another process could still modify files. A detached
   verification worktree (Milestone 3) closes that window. Projects without
   a Git tree record no revision and get no evidence reuse.

5a. **Project-level regression changes do not re-judge completed tasks.**
   Only task-level policy edits withdraw completion. Use
   `tasks verify --again` (with `--no-reuse` to force execution rather than
   evidence reuse) to re-verify deliberately.

6. **Withdrawing completion does not evict released dependents.** If a
   policy change (Milestone 2) invalidates a completed task's evidence,
   dependents already taken keep their attempts. They will be blocked on
   their next take.

7. **Single machine, local filesystem.** WAL-mode SQLite on a network
   filesystem is unsafe; multi-host access is unsupported.

8. **Automatic selection is first-come.** There is no priority field;
   automatic take prefers interrupted, then failed, then oldest. Agents that
   need ordering should take by ID.

9. **Ambiguous project names.** Resolution by name fails when two projects
   share a name; use the ID.

10. **Clock skew.** Lease expiry compares the engine's clock with stored
    timestamps. Separate processes on one machine share a clock; a manually
    moved clock can expire or extend leases.
