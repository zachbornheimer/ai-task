# Implementation status against the `at` handoff

Milestones from the handoff (section 12) and the acceptance matrix
(section 11), with the test that proves each row. "Unsupported" rows are
documented limitations, not silent gaps.

## Milestones

| Milestone | Status |
|---|---|
| M0 preserve/correct the engine | done: fail-closed completion, no evidence reuse, prerequisite/contract/policy recheck at finalisation, regression namespace separation, token fencing and replay, CI workflow |
| M1 planning/import API | done: keys, groups, atomic `Apply` with optimistic revision and idempotency, typed `List`/`Show`, deterministic selector, planner authority rules, `at add/update/show/list` with the circle glyphs |
| M2 execution/verification contract | done: `claim`/`claim renew`/`claim release`/`log`/`verify task\|regression\|complete`, per-task worktrees (`at/<id>`, reused across attempts), attempt-bound tokens (never on disk), mandatory required checks at `add` and `claim`, prerequisite learnings and last-failure evidence in the handoff, detached snapshots, finalisation transaction, stable JSON, `Claim(wait)` with `DONE`/`STALLED`/cancel, persistent failures and cooldowns |
| M3 integration and coupled verification | done: guarded two-phase promotion with compare-and-swap and bounded retry on a moved base; durable leased cohort jobs with crash recovery; no DAG cycles for cohorts |
| M4 reference example | done: `examples/embedded_runner` with fake planner, reviewer and coding agent; its test drains a seven-task graph (including a cohort) with four workers |

## Acceptance matrix

| Test | Proof |
|---|---|
| Atomic proposal | `TestAtomicProposalInsertsNothingOnInvalidEdge` |
| Stable retry | `TestStableKeysAndIdempotency` |
| Review mutation | `TestReviewMutationIsOneRevisionWithOptimisticConcurrency` |
| No priority | `TestClaimIsDeterministicOldestFirst`; no priority field exists |
| Concurrent claims | `TestConcurrentClaimsYieldDistinctSessions` (20 callers, 5 tasks), `e2e.TestConcurrentProcessesClaimDistinctTasks` |
| Explicit claim | `TestExplicitClaimEnforcesEligibility` |
| Token fencing | `TestTokenFencingAcrossExpiryAndSupersession` |
| Claim heartbeat | engine-side heartbeat during verification (`heartbeat`, `jobHeartbeat`); the host-side cancel-on-renewal-failure is shown in `examples/embedded_runner` and is host code (see "Unsupported") |
| Continuous queue | `TestContinuousQueueReleasesDependentsImmediately` |
| `ErrDone` vs `ErrStalled` | `TestWaitingClaimDoneAndStalled`, `TestCoupledVerificationRecoversAfterVerifierCrash` (pending cohort is neither) |
| Empty checks | `TestEmptyChecksFailClosed` |
| Provisional checks | `TestProvisionalChecksRunFreshAndNeverComplete` |
| No evidence reuse | `TestNoEvidenceReuseAndSameSnapshot`, `TestGoTestCacheDisabledInCheckEnvironment` |
| Same snapshot | `TestNoEvidenceReuseAndSameSnapshot`, `workspace.TestTaskWorktreesAndSnapshots` |
| Parallel isolation | not implemented: suites run sequentially on one snapshot (see "Unsupported") |
| Final success | `TestFinalSuccessIsAtomicAndPromotes` |
| Final failure | `TestFinalFailureKeepsClaimAndBlocksDownstream` |
| Late blocker | `TestLateBlockerPreventsCompletion` |
| Completed-edge mutation | `TestCompletedContractsCannotChangeSilently` |
| Regression ID collision | `TestRegressionNamespaceIsSeparate` |
| Worker crash/restart | `TestVerifierCrashRecovery`, `TestTokenFencingAcrossExpiryAndSupersession` |
| Coupled verification | `TestCoupledVerificationCompletesTogether`, `TestCoupledVerificationFailureSendsFailingMemberBack`, `TestCoupledVerificationRecoversAfterVerifierCrash` |
| Integration conflict | `TestIntegrationConflictDoesNotComplete`, `workspace.TestPrepareMergeAndPromote` |
| Idempotent completion ack | `TestFinalSuccessIsAtomicAndPromotes` (replay section) |
| CLI rendering | `cli.TestListGolden`, `cli.TestShowGolden`, `cli.TestAckAndVerifyRendering` (`internal/cli/testdata/*.txt`), `e2e.TestAgentLoopEndToEnd` |
| CLI security | `e2e.TestAgentLoopEndToEnd` (no tokens in read views; renew/release reject missing and stale tokens everywhere; a worktree never supplies one; a token for another task is refused in a worktree; JSON parseable) |

## Invariants (handoff section 2)

| # | Invariant | Status |
|---|---|---|
| 1 | Task-ID reads never convey authority | holds: every mutation needs a token or planner authority |
| 2 | Token writes only while current, unexpired, not ended, not superseded; validated in the write transaction | holds: `execution.Authorize` inside each `Write` |
| 3 | No completion with unfinished requires, missing checks, stale policy/version, unintegrated code, or mismatched revision | holds: `executeFinal` finalisation transaction and `executeCohortJob` |
| 4 | Completion and claim end are one transaction | holds |
| 5 | Released claim is available again unless blocked, awaiting cohort, or in cooldown | holds: `TestReleaseAndCooldown`, `TestDiscoveredBlockerNeedsSessionOrPlanner` |
| 6 | Parent/child never becomes a hard edge | holds: `TestGroupsAreOrganizationalOnly` |
| 7 | Edits to claimed/completed contracts cannot keep old evidence | holds: completed tasks reject edits; claimed-task contract edits need the planner and fail a running verification closed (`TestContractChangeDuringVerificationFailsClosed`) |

## Unsupported or compromised in this implementation

- **Parallel task/regression execution.** Both suites run sequentially on
  the same snapshot. Nothing can prove two arbitrary suites are isolated,
  so the safe default is sequential; a declared-isolation flag is a future
  addition.
- **Host-side process control.** The engine renews the lease on every
  authenticated command, but it cannot stop a stale agent process from
  editing files. Per-task worktrees
  limit the blast radius; the host must cancel the agent when `Renew`
  fails (shown in the example).
- **Human gates.** No human attestation path; a task without runnable
  checks cannot complete. Model such work as a task whose check verifies
  the artefact of the human action (a file, a config value).
- **Trust.** `--planner` and `AT_SESSION` are a convention for the local
  OS user. Same-user processes can edit the SQLite file or the repository.
- **Check side effects.** Checks run in a disposable snapshot with
  `GOFLAGS=-count=1`; the engine cannot prove a check does not touch
  shared services or write outside the snapshot.
- **Worktree cleanup.** Task worktrees and `at/<id>` branches are kept
  (the next attempt reuses them; an agent's shell may still be inside
  one). No automatic pruning.
- **Git only.** Non-Git projects were removed; `at init` creates a
  repository when needed.
- **Regression policy edits do not re-judge completed tasks**; they apply
  to future runs. Task-contract edits on completed tasks are rejected.
- **Cohort failure attribution.** A failing shared regression sends every
  member back; a failing member task check sends that member back and
  leaves passing peers waiting. No automatic blame beyond that.
- **Single host.** SQLite on a local filesystem.
