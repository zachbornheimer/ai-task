# Implementation status against the `at` handoff

Milestones from the handoff (section 12) and the acceptance matrix
(section 11), with the test that proves each row. "Unsupported" rows are
documented limitations, not silent gaps.

## Milestones

| Milestone                                           | Status                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| --------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| M0 preserve/correct the engine                      | done: fail-closed completion, no evidence reuse, prerequisite/contract/policy recheck at finalisation, regression namespace separation, token fencing and replay, CI workflow                                                                                                                                                                                                                                                                                                                    |
| M1 planning/import API                              | done: keys, groups, atomic `Apply` with optimistic revision and idempotency, typed `List`/`Show`, deterministic selector, planner authority rules, `atask add/update/show/list` with the circle glyphs                                                                                                                                                                                                                                                                                           |
| M2 execution/verification contract                  | done: `claim`/`claim renew`/`claim release`/`log`/`verify task\|regression\|complete`, per-task worktrees (`atask/<id>`, reused across attempts), worktree-resident tokens with quarantine of unended attempts' worktrees on takeover, mandatory required checks at `add` and `claim`, prerequisite learnings and last-failure evidence in the handoff, detached snapshots, finalisation transaction, stable JSON, `Claim(wait)` with `DONE`/`STALLED`/cancel, persistent failures and cooldowns |
| M3 integration and coupled verification             | done: durable integration intents (fenced re-check, descriptor-owned promotion lock, compare-and-swap, completion keyed on the intent, reconciliation from Git after a crash) for single tasks and cohorts; durable leased cohort jobs with crash recovery, pinned submitted contracts, idempotent acknowledgement, per-member blame and capped promotion backoff; no DAG cycles for cohorts                                                                                                     |
| M5 corrective directive (P0-A..F, P1-A..H, Phase 3) | done on the work branch: required gates everywhere, quarantine on takeover, intents, fenced and classified failure accounting, no head-of-line blocking, one eligibility rule, payload-bound idempotency, category isolation, safe fast-forward into dirty checkouts, resilient heartbeat, progress glyphs, CLI corners, prune. See docs/invariants.md for the test behind each.                                                                                                                 |
| M4 reference example                                | done: `examples/embedded_runner` with fake planner, reviewer and coding agent; its test drains a seven-task graph (including a cohort) with four workers                                                                                                                                                                                                                                                                                                                                         |

## Dogfooding pilot

Two rounds of three Sonnet coding agents each drove `at` on a scratch Go
project (four and five tasks, concurrent claims, every task editing the
same dispatch site, checks kept outside the repository). Nine tasks
completed, none incorrectly, with no double claim, no lost work and every
refusal carrying evidence. Round one produced the wording and claim-result
changes in `0b0075c`. Round two accidentally ran on a target branch whose
regression suite failed; the agents still finished, and the lesson is a
planning rule rather than an engine change: make the regression suite pass
on the target before adding tasks, add a scaffolding task that siblings
require when they share a dispatch site, and guard against stray build
artifacts with a check.

### Round 3: the harness routes a plain request through `atask` on its own

A scratch repository set up with `atask init --claude` (instruction block,
CLAUDE.md import, Claude Code hooks) and three regression checks. A
headless Claude Code session was given a plain feature request that never
mentioned `at`. Transcript evidence: the session-start hook injected the
plan context; the agent ran `atask doctor`, then `atask add ... --claim` (its
first attempt hit the check-id collision guard and it corrected itself),
edited only inside the task worktree, committed, ran `atask verify
complete`, and the stop hook let the turn end because the claim was
finished. Five turns, twenty seconds; the work was on `main` with the
engine's record of the passing run. A second session was told to edit
README.md directly on `main` and skip the engine: the edit guard denied
the Edit with the reason and the remedy, the agent stopped and offered the
`atask add --claim` path instead, and `main` was untouched. What the runs do
not show: a long interactive session, and an agent that edits with
shell scripts instead of the Edit tool bypasses the guard (the engine's
own gates still hold; only the convenience of early refusal is lost).

### Round 4: `at` builds `at`

The prime-time review's findings were fixed through `atask` on this
repository: a plan (`docs/plans/prime-time.yaml`) of 20 tasks under one
epic, worked by parallel coding agents and by hand, each task with a
proof check that failed before the change and passed after, gated by the
repository's own regression suite (gofmt, go vet, go test). Every task
completed through `atask verify complete`; the epic's integration check ran
once on the target when the last member landed. Later fixes (merge
identity, run logs, first-contact CLI, onboarding friction, these docs)
went the same way. What it found in `at` itself:

- A doubled `GOFLAGS=-count=1` turned the project's own suite red under
  `at` (found by `atask doctor`, fixed first).
- Workers were charged failures for a cancelled database write; a
  cancelled write is now `CANCELLED`, never a strike.
- Integration merges were committed as a fixed `atask <at@localhost>`; they
  now use the repository's identity and carry `At-Task:` trailers.
- Failure tails lost `go test`'s FAIL lines in the middle of long output;
  every run now writes complete logs and `result.json` (`log_dir`).
- A `verify complete` whose target moved re-runs the regression suite on
  the merge; with a 2-minute gate this is the dominant cost of a busy
  plan. Not changed: the merge is what gets promoted, so it is what must
  pass.

### Fresh install

A new agent with no context installed `at` with `go install
…/cmd/atask@main` (through the default module proxy, first try), built a
small Go project, and drove two tasks to completion from the README
alone, including a deliberate failure, a release and re-claim with the
handoff, and an integration merge after the target moved. It found no
blocker. Its first-contact friction was fixed: `atask --help` exited 2,
`atask version` said `dev`, shell syntax such as `$(gofmt -l .)` in a plain
check was silently tested as literal text (now refused with the `sh -c`
remedy), the tour did not say to commit the files `atask init` writes,
`atask prune` left `.tmp` directories, and a merge-verified run listed each
check twice unlabelled. Documented rather than changed: `DONE`/`STALLED`
exit 1, failed attempts' commits stay in history, and a check that
passes before the work exists (`go test -run` with no matching test)
proves nothing.

## Acceptance matrix

| Test                      | Proof                                                                                                                                                                                                              |
| ------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Atomic proposal           | `TestAtomicProposalInsertsNothingOnInvalidEdge`                                                                                                                                                                    |
| Stable retry              | `TestStableKeysAndIdempotency`                                                                                                                                                                                     |
| Review mutation           | `TestReviewMutationIsOneRevisionWithOptimisticConcurrency`                                                                                                                                                         |
| No priority               | `TestClaimIsDeterministicOldestFirst`; no priority field exists                                                                                                                                                    |
| Concurrent claims         | `TestConcurrentClaimsYieldDistinctSessions` (20 callers, 5 tasks), `e2e.TestConcurrentProcessesClaimDistinctTasks`                                                                                                 |
| Explicit claim            | `TestExplicitClaimEnforcesEligibility`                                                                                                                                                                             |
| Token fencing             | `TestTokenFencingAcrossExpiryAndSupersession`                                                                                                                                                                      |
| Claim heartbeat           | engine-side heartbeat during verification (`heartbeat`, `jobHeartbeat`); the host-side cancel-on-renewal-failure is shown in `examples/embedded_runner` and is host code (see "Unsupported")                       |
| Continuous queue          | `TestContinuousQueueReleasesDependentsImmediately`                                                                                                                                                                 |
| `ErrDone` vs `ErrStalled` | `TestWaitingClaimDoneAndStalled`, `TestCoupledVerificationRecoversAfterVerifierCrash` (pending cohort is neither)                                                                                                  |
| Empty checks              | `TestEmptyChecksFailClosed`                                                                                                                                                                                        |
| Provisional checks        | `TestProvisionalChecksRunFreshAndNeverComplete`                                                                                                                                                                    |
| No evidence reuse         | `TestNoEvidenceReuseAndSameSnapshot`, `TestGoTestCacheDisabledInCheckEnvironment`                                                                                                                                  |
| Same snapshot             | `TestNoEvidenceReuseAndSameSnapshot`, `workspace.TestTaskWorktreesAndSnapshots`                                                                                                                                    |
| Parallel isolation        | not implemented: suites run sequentially on one snapshot (see "Unsupported")                                                                                                                                       |
| Final success             | `TestFinalSuccessIsAtomicAndPromotes`                                                                                                                                                                              |
| Final failure             | `TestFinalFailureKeepsClaimAndBlocksDownstream`                                                                                                                                                                    |
| Late blocker              | `TestLateBlockerPreventsCompletion`                                                                                                                                                                                |
| Completed-edge mutation   | `TestCompletedContractsCannotChangeSilently`                                                                                                                                                                       |
| Regression ID collision   | `TestRegressionNamespaceIsSeparate`                                                                                                                                                                                |
| Worker crash/restart      | `TestVerifierCrashRecovery`, `TestTokenFencingAcrossExpiryAndSupersession`                                                                                                                                         |
| Coupled verification      | `TestCoupledVerificationCompletesTogether`, `TestCoupledVerificationFailureSendsFailingMemberBack`, `TestCoupledVerificationRecoversAfterVerifierCrash`                                                            |
| Integration conflict      | `TestIntegrationConflictDoesNotComplete`, `workspace.TestPrepareMergeAndPromote`                                                                                                                                   |
| Idempotent completion ack | `TestFinalSuccessIsAtomicAndPromotes` (replay section)                                                                                                                                                             |
| CLI rendering             | `cli.TestListGolden`, `cli.TestShowGolden`, `cli.TestAckAndVerifyRendering` (`internal/cli/testdata/*.txt`), `e2e.TestAgentLoopEndToEnd`                                                                           |
| CLI security              | `e2e.TestAgentLoopEndToEnd` (no tokens in read views; renew/release reject missing and stale tokens outside a worktree and need none inside it; a token for another task is refused in a worktree; JSON parseable) |

## Invariants (handoff section 2)

| # | Invariant                                                                                                               | Status                                                                                                                                                                       |
| - | ----------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1 | Task-ID reads never convey authority                                                                                    | holds: every mutation needs a token or planner authority                                                                                                                     |
| 2 | Token writes only while current, unexpired, not ended, not superseded; validated in the write transaction               | holds: `execution.Authorize` inside each `Write`                                                                                                                             |
| 3 | No completion with unfinished requires, missing checks, stale policy/version, unintegrated code, or mismatched revision | holds: `executeFinal` finalisation transaction and `executeCohortJob`                                                                                                        |
| 4 | Completion and claim end are one transaction                                                                            | holds                                                                                                                                                                        |
| 5 | Released claim is available again unless blocked, awaiting cohort, or in cooldown                                       | holds: `TestReleaseAndCooldown`, `TestDiscoveredBlockerNeedsSessionOrPlanner`                                                                                                |
| 6 | Parent/child never becomes a hard edge                                                                                  | holds: `TestGroupsAreOrganizationalOnly`                                                                                                                                     |
| 7 | Edits to claimed/completed contracts cannot keep old evidence                                                           | holds: completed tasks reject edits; claimed-task contract edits need the planner and fail a running verification closed (`TestContractChangeDuringVerificationFailsClosed`) |

## Unsupported or compromised in this implementation

- **Parallel task/regression execution.** The two suites run
  sequentially, each on its own snapshot of the revision; nothing can
  prove two arbitrary suites are safe to run at once, so a
  declared-isolation flag is a future addition.
- **Host-side process control.** The engine renews the lease on every
  authenticated command, but it cannot stop a stale agent process from
  editing files. Per-task worktrees
  limit the blast radius; the host must cancel the agent when `Renew`
  fails (shown in the example).
- **Human gates.** No human attestation path; a task without runnable
  checks cannot complete. Model such work as a task whose check verifies
  the artefact of the human action (a file, a config value).
- **Trust.** Planner authority (no task token) and `ATASK_SESSION` are a convention for the local
  OS user. Same-user processes can edit the SQLite file or the repository.
- **Check side effects.** Checks run in a disposable snapshot with
  `GOFLAGS=-count=1`; the engine cannot prove a check does not touch
  shared services or write outside the snapshot.
- **Worktree cleanup.** A task's quarantined directories are removed when
  it completes; `atask prune` removes worktrees of complete and archived
  tasks and idle tasks' quarantines. `atask/<id>` branches are kept.
- **Git only.** Non-Git projects were removed; `atask init` creates a
  repository when needed.
- **Regression policy edits do not re-judge completed tasks**; they apply
  to future runs. Task-contract edits on completed tasks are rejected.
- **Cohort failure attribution.** A failing member task check sends that
  member back (one counted failure) while passing peers wait; a failing
  shared regression sends every member back with the evidence and charges
  nobody; a late conflict sends back only the member it concerns. Which
  member broke a shared suite is not inferred.
- **Single host.** SQLite on a local filesystem.
- **Platforms.** Linux is tested (CI and the pilots); macOS and Windows
  cross-compile but are not exercised.
- **Known flaky test.** `TestCoupledVerificationRecoversAfterVerifierCrash`
  has failed twice under full-suite load (a member was claimable after
  the recovered cohort job, where `DONE` was expected; the failing run
  took about 6s against 0.8s normally) and did not reproduce in over 130
  loaded runs. The test now prints each member's status, failure count
  and last error when it fails, so the next occurrence names the path.
