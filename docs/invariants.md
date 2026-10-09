# Invariants

Each invariant names the test that proves it (`internal/app/*_test.go`
unless noted).

## Identity and authority

- Task IDs (`at-` + 6 base32 chars), project IDs and session tokens (160
  bits) are random, never content-derived, never reused.
  (`task.TestIDs`)
- Task-ID lookups and reads never convey authority. Every mutation needs a
  session token (execution) or planner authority (planning).
- A token writes only while its attempt is current (fencing generation),
  unexpired, not ended, and not superseded; the check runs in the same
  transaction as the write. (`TestTokenFencingAcrossExpiryAndSupersession`)
- Tokens are stored as digests and never rendered.
  (`cli.TestShowGolden`, `e2e.TestAgentLoopEndToEnd`)

## Plan

- A ChangeSet is one atomic revision: any invalid operation leaves the plan
  untouched and the revision unchanged. (`TestAtomicProposalInsertsNothingOnInvalidEdge`)
- `ExpectedPlanRev` mismatch is `PLAN_CONFLICT`. Identical replays by
  idempotency key or by stable key create nothing; conflicting definitions
  under an existing key are `DUPLICATE_KEY`. (`TestStableKeysAndIdempotency`)
- Hard edges form a DAG; validation and insertion share the write
  transaction. (`dependency.TestValidate`, `TestReviewMutationIsOneRevisionWithOptimisticConcurrency`)
- Groups are organizational: never claimable, never an edge endpoint,
  progress derived, empty groups never complete, the hierarchy is a tree.
  (`TestGroupsAreOrganizationalOnly`)
- Task checks and regression checks are separate namespaces; collisions
  are rejected in both directions. (`TestRegressionNamespaceIsSeparate`)
- Completed tasks accept no contract or edge changes and cannot be
  archived. (`TestCompletedContractsCannotChangeSilently`)
- Eligibility changes to a claimed task need its session or the planner;
  contract changes need the planner. (`TestDiscoveredBlockerNeedsSessionOrPlanner`)

## Status derivation

Precedence: archived > complete > awaiting_integration > verifying >
awaiting_verification > claimed > blocked > needs_attention >
cooldown > verification_failed > interrupted > ready (a cooling task is
not claimable however its last proof ended). Claimable: ready,
interrupted, verification_failed. Active (keeps a waiting worker waiting):
claimed, verifying, and submissions whose cohort can be verified.
(`task.TestDerivePrecedence`, `TestWaitingClaimDoneAndStalled`)

## Claims

- Claim selects and leases in one immediate transaction; concurrent
  callers get distinct sessions. (`TestConcurrentClaimsYieldDistinctSessions`, `e2e.TestConcurrentProcessesClaimDistinctTasks`)
- Oldest eligible first, stable tie-break; no priority exists.
  (`TestClaimIsDeterministicOldestFirst`)
- Explicit claims enforce the same eligibility. (`TestExplicitClaimEnforcesEligibility`)
- Completing A makes its dependents claimable before unrelated work ends.
  (`TestContinuousQueueReleasesDependentsImmediately`)
- `Claim(wait)` returns `DONE` only when every executable task is complete,
  `STALLED` only when nothing is claimable, nothing (including a runnable
  cohort job or an open integration) is in flight, and nothing is in
  retry cooldown, and `ctx.Err()` on cancellation. A cooldown is waited
  for. (`TestCooldownIsOneRuleEverywhere`)
- The SQL claim prefilter and the derived status agree on claimability
  for every state the engine produces.
  (`TestClaimablePrefilterMatchesDerivedStatus`)
  (`TestWaitingClaimDoneAndStalled`, `TestCoupledVerificationRecoversAfterVerifierCrash`)
- Release keeps the handoff; `--failed` counts a failure and applies the
  cooldown; exhausted tasks need a planner reset. (`TestReleaseAndCooldown`)
- One unusable worktree never blocks the queue: the task is marked
  needs_attention with the reason, excluded from automatic selection (SQL
  and status agree), retried by an explicit claim, cleared by a planner
  reset; `list ready`, `status` and `claim` agree throughout.
  (`TestUnusableWorktreeDoesNotBlockTheQueue`)
- A consumed cohort submission token is acknowledged idempotently and
  gains no authority; a submission awaiting its peers pins the member's
  contract until an explicit planner withdrawal; a late conflict sends
  back only the member it concerns; a shared regression failure charges
  nobody; a promotion that cannot land backs off (bounded, cooling, not a
  stall) and completes once the cause is gone.
  (`TestCohortSubmissionIsAcknowledgedIdempotently`,
  `TestSubmittedCohortContractIsPinned`,
  `TestCohortLateConflictBlamesOnlyThatMember`,
  `TestCohortRegressionFailureChargesNobody`,
  `TestCohortPromotionBackoffIsBounded`)
- Glyphs reflect historical progress: ○ never started, ◐ started but not
  complete (including awaiting verification, failed, and a blocked task
  with a past attempt), ● complete; a group is ◐ as soon as one member
  started. (`cli.TestListGolden`: list.txt, list_glyphs.txt, list_nested.txt)
- An idempotency key is bound to the request's content digest; a replay
  under a stable key compares blockers as well; a change set that changes
  nothing does not bump the plan revision.
  (`TestIdempotencyIsBoundToThePayload`)
- Failure accounting is fenced and classified: a failed proof counts once
  per current attempt; environment and authority problems are recorded as
  `last_error`, never counted; a stale attempt cannot count against its
  successor; a double-fired completion is refused, not raced.
  (`TestFailureAccountingIsFencedAndClassified`)
- No claim without a project regression suite; no task without task
  checks; a check set cannot be emptied. (`TestEmptyChecksFailClosed`,
  `e2e.TestPlanningCommands`)
- One worktree per task on `at/<id>`, reused across clean handoffs; the
  claim stores the token in the worktree's Git directory, never in the
  tree, and commands run there need no token. A takeover quarantines the
  unended attempt's worktree (moved, detached) before the new attempt
  gets a fresh one: the stale holder's token is refused everywhere, its
  Git directory never holds the successor's token, and its later commits
  never reach the branch or the new worktree. A token used inside another
  task's worktree is refused. Every authenticated command renews the
  lease.
  (`TestTakeoverLeavesStaleTokenPowerless`, `e2e.TestAgentLoopEndToEnd`,
  `e2e.TestDiscoveredBlockerFromWorktree`)
- The handoff carries prerequisites' learnings and the newest failed
  run's evidence. (`TestHandoffCarriesPrerequisiteLearningsAndLastFailure`)
- Concurrent promotions into a checked-out target: exactly one wins, the
  rest see a moved target and rebuild, and the checkout stays clean.
  (`workspace.TestConcurrentPromotionsIntoCheckout`)
- A promotion into a dirty checkout proceeds when it touches none of the
  dirty paths and refuses, changing nothing, when it would; local files
  are never overwritten. (`workspace.TestPromoteIntoDirtyCheckoutIsSafe`)
- Task checks and regression checks run on separate snapshots; a file a
  task check leaves behind is not there for the regression suite; checks
  never see `AT_SESSION`. (`TestCategoriesDoNotShareASnapshot`)
- The target moves only after a fenced intent transaction re-checked
  everything completion depends on, and completion is recorded exactly
  when the candidate reached the target: a crash at any point is resolved
  from Git by reconciliation (complete, or claimable with no failure
  counted); a contract change or a superseded session during verification
  never moves the target; while an intent is open the task cannot be
  claimed, edited, blocked or archived.
  (`TestIntegrationIntentCrashRecovery`, `TestCohortIntentCrashRecovery`,
  `TestPlannerEditDuringVerificationNeverMovesTarget`,
  `TestSupersededVerifierCannotPromote`)

## Verification and completion

- `at verify` with no mode is invalid. (`TestBareVerifyIsInvalid`)
- Missing task checks or missing regression checks fail closed.
  (`TestEmptyChecksFailClosed`)
- Diagnostic runs execute every time and never complete, release, or
  integrate. (`TestProvisionalChecksRunFreshAndNeverComplete`)
- No run reuses prior evidence; Go's test cache is disabled for checks.
  (`TestNoEvidenceReuseAndSameSnapshot`, `TestGoTestCacheDisabledInCheckEnvironment`)
- Final runs execute on a detached snapshot of the submitted revision; the
  worktree must be clean. (`TestNoEvidenceReuseAndSameSnapshot`)
- Completion and claim end commit together after re-checking authority,
  prerequisites, contract revision, regression policy, and evidence on the
  final revision. (`TestFinalSuccessIsAtomicAndPromotes`, `TestLateBlockerPreventsCompletion`, `TestContractChangeDuringVerificationFailsClosed`)
- Failure leaves the task incomplete with the claim live, records the
  failure, and releases nothing downstream. (`TestFinalFailureKeepsClaimAndBlocksDownstream`)
- Promotion never advances the target on conflict or on a moved base
  without re-verification; a merge that changed content is verified before
  promotion. (`TestIntegrationConflictDoesNotComplete`, `workspace.TestPrepareMergeAndPromote`)
- A crashed verifier leaves no phantom state: expired leases close runs,
  and another worker resumes. (`TestVerifierCrashRecovery`)
- A committed completion is acknowledged idempotently to the same token
  without a new run. (`TestFinalSuccessIsAtomicAndPromotes`)
- Cohorts: members are claimable concurrently; the first submitter waits;
  one candidate is verified fresh for all; all complete atomically or the
  failing member is sent back; a dead verifier's job is retried by any
  worker. (`cohort_test.go`)
