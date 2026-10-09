# Invariants

Each invariant names the test that proves it. Tests live beside the package
(`*_test.go`), in `internal/app/engine_test.go` (engine), or in `test/e2e`.

## Identity

- Task, project, and session identifiers are random (80 / 80 / 160 bits),
  never derived from content, never reused. Collisions are detected by
  UNIQUE constraints and retried. (`task.TestIDs`, `project.TestIDs`)
- Session tokens are stored only as SHA-256 digests and never appear in any
  read model. (`e2e.TestWorkflowAcrossProcesses` greps every read command)

## Dependency graph

- Edge direction is single: `Edge{Task: B, Requires: A}` ⇔ `tasks deps add B
  --requires A` ⇔ "B requires A". (`dependency.TestValidate`)
- No self-dependency (`SELF_DEPENDENCY`), no duplicate edge
  (`DUPLICATE_DEPENDENCY`), no cross-project edge
  (`CROSS_PROJECT_DEPENDENCY`), no cycle (`DEPENDENCY_CYCLE`).
  (`engine.TestDependencyInvariants`)
- Validation and insertion share one immediate transaction, so concurrent
  writers cannot close a cycle. (`engine.TestConcurrentEdgeAddsCannotFormCycle`)
- A batch of edges is all-or-nothing. (`engine.TestDependencyInvariants`)
- Removing an edge takes effect on the next read; nothing is recomputed or
  stored. (`engine.TestDependencyInvariants`)
- A prerequisite is satisfied iff it is complete.

## Status derivation

Precedence, highest first:

| Status | Fact |
|---|---|
| `complete` | `completed_at` set |
| `awaiting_integration` | newest run passed, not complete |
| `awaiting_verification` | newest submission has no finished run |
| `in_progress` | current attempt's lease active |
| `blocked` | ≥1 prerequisite not complete |
| `verification_failed` | newest run failed/errored |
| `interrupted` | current attempt open, lease expired |
| `available` | none of the above |

`in_progress` outranks `blocked` so a dependency added mid-attempt does not
hide the active work; `blocked` outranks the recoverable states so no agent
is offered work it cannot start. (`task.TestDerivePrecedence`)

Takeable statuses: `available`, `interrupted`, `verification_failed`.
Automatic selection order: interrupted → verification_failed → available,
oldest first. The SQL pre-filter is re-checked by `task.Derive` before the
claim. (`engine.TestConcurrentAutomaticTakeAssignsEachTaskOnce`)

## Execution authority

- Exactly one attempt per task can hold authority: the attempt whose `seq`
  equals the task's `current_attempt_seq`, not ended, lease unexpired. The
  generation advance is a conditional UPDATE. (`engine.TestConcurrentTakeOfOneTaskYieldsOneAuthority`, `e2e.TestConcurrentProcessesClaimOnce`)
- Authorisation and the mutation it guards commit in the same transaction.
  (`app.authorize` is only callable inside `Write`)
- Expired tokens cannot log, renew, or finish (`LEASE_EXPIRED`); a later
  attempt fences out earlier tokens permanently (`SESSION_SUPERSEDED`); a
  finished attempt's token is inert (`SESSION_FINISHED`).
  (`engine.TestCrashAndResumeFromAnotherProcess`, `e2e.TestLeaseExpiryAndResumeAcrossProcesses`)
- Lease bounds: 1 s ≤ lease ≤ 24 h, default 1 h. Renewal is one conditional
  UPDATE. (`execution.TestLeaseDuration`)
- A lease proves authority over *task state*, not that a process is running
  or has stopped touching files. (`docs/limitations.md`)

## Log and handoff

- Entries are append-only, tied to task and attempt, ordered by a global
  sequence, and never edited or compacted. (`engine.TestCrashAndResumeFromAnotherProcess`)
- An entry needs at least one non-empty field. (`execution.TestLogEntryValidate`)
- An acknowledged entry is committed. (`engine.TestLogsSurviveEngineClose`)
- `take` and `show` return at most 10 recent entries, the latest `next`, and
  at most 50 learnings, plus the total count; `history` pages the rest. A
  log over 200 entries produces a warning, not a limit.

## Submission and completion

- One submission per attempt (UNIQUE); `finish` is idempotent per token.
  (`engine.TestLifecycleDependencyReleasesOnCompletion`)
- A submission records the merged policy (task + project regression) and
  its digest. (`engine.TestProjectRegressionMergesIntoSubmission`)
- A task is complete only when its newest submission's newest run is
  `passed` and the integration policy is satisfied. A run passes iff every
  required check has `passed` evidence; optional checks never compensate;
  a missing, erroring, timed-out or skipped required check fails the run.
  (`verification.TestJudge`, `engine.TestFailingRequiredCheckPreventsCompletionAndPreservesWork`, `engine.TestOptionalCheckCannotCompensateForRequiredFailure`, `engine.TestMissingVerifierAndTimeoutAreNotPassed`)
- Checks never run inside a database transaction; each evidence row commits
  on its own. (`e2e.TestChecksRunWithoutHoldingTheDatabase`, `engine.TestInterruptedVerificationIsRecoverable`)
- Evidence is bound to the revision and check content; reuse requires both
  to match and the prior outcome to be `passed`. Evidence is never produced
  on a tree that is dirty or not at the submitted revision.
  (`engine.TestEvidenceReuseIsBoundToRevisionAndCheckContent`, `engine.TestVerificationRefusesTreeThatDoesNotMatchSubmission`)
- A Git submission names a clean commit; nothing is staged or committed on
  the agent's behalf. (`engine.TestDirtyWorktreeSubmissionIsRejected`)
- A task-level policy change appends a `stale` run and withdraws
  completion; the policy is not reachable through a session token. While
  an attempt is open the change applies to that attempt's next submission
  instead, so the attempt stays visible.
  (`e2e.TestDeferredVerificationAndPolicyChange`, `engine.TestPolicyChangeDuringOpenAttemptKeepsAttemptVisible`)
- `verify` refuses while an attempt is open, and recording a new submission
  withdraws any completion fact, so the task is complete only on the
  evidence of its newest submission.
  (`engine.TestVerifyRefusesWhileAttemptOpenAndNewSubmissionWithdrawsCompletion`)
- Finish reports authority errors before workspace errors.
  (`engine.TestFinishReportsAuthorityBeforeDirtyTree`)
- A task awaiting verification or integration cannot be taken.
- Withdrawing completion (`ClearComplete`, used by Milestone 2 policy
  changes) does not retroactively evict attempts on dependents that were
  already released; it only blocks future takes. Documented limitation.

## Project resolution

- A project's identity is its random ID; its root path is a mutable
  resolution hint. (`project.TestValidate`)
- Any linked Git worktree resolves to the main worktree's project without
  invoking Git. (`project.TestLocateRootMainAndLinkedWorktree`, `e2e.TestCommandsWorkFromLinkedWorktree`)
- Resolution precedence: `--project`/`TASKS_PROJECT` (ID or unique name) →
  repository root → longest registered root prefix → `NO_PROJECT`.
  (`engine.TestProjectResolutionAcrossWorktrees`)
