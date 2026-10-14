# Implementation status against the `at` handoff

Milestones from the handoff (section 12) and the acceptance matrix
(section 11), with the test that proves each row. "Unsupported" rows are
documented limitations, not silent gaps.

## Milestones

| Milestone                                           | Status                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| --------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| M0 preserve/correct the engine                      | done: fail-closed completion, no evidence reuse, prerequisite/contract recheck at finalisation, token fencing and replay, CI workflow                                                                                                                                                                                                                                                                                                                                                 |
| M1 planning/import API                              | done: keys, groups, atomic `Apply` with optimistic revision and idempotency, typed `List`/`Show`, deterministic selector, planner authority rules, `atask add/update/show/list` with the circle glyphs                                                                                                                                                                                                                                                                                |
| M2 execution/verification contract                  | done: `claim`/`claim renew`/`claim release`/`log`/`verify task\|regression\|complete`, per-task worktrees (`atask/<id>`, reused across attempts), worktree-resident tokens with quarantine of unended attempts' worktrees on takeover, both proofs required before `claim`, prerequisite learnings and last-failure evidence in the handoff, detached snapshots, finalisation transaction, stable JSON, `Claim(wait)` with `DONE`/`STALLED`/cancel, persistent failures and cooldowns |
| M3 groups and stacked branches                      | done: a group's proofs run on the merge of every child's completed revision; dependents' branches start from the target branch plus a merge of every required task's completed revision (`BASE_CONFLICT` at claim); completion records the revision and never moves the target; `on_complete` actions                                                                                                                                                                                 |
| M5 corrective directive (P0-A..F, P1-A..H, Phase 3) | done on the work branch: both proofs required everywhere, quarantine on takeover, fenced and classified failure accounting, no head-of-line blocking, one eligibility rule, payload-bound idempotency, category isolation, resilient heartbeat, progress glyphs, CLI corners, prune. See docs/invariants.md for the test behind each.                                                                                                                                                 |
| M4 reference example                                | done: `examples/embedded_runner` with fake planner, reviewer and coding agent; its test drains a seven-task graph (including a group) with four workers                                                                                                                                                                                                                                                                                                                               |

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

A scratch repository set up with `atask init` (instruction block and
CLAUDE.md import). A headless Claude Code session was given a plain feature
request that never mentioned `at`. Transcript evidence: the agent ran
`atask doctor`, then `atask add ...` and `atask claim`, edited only inside
the task worktree, committed, and ran `atask verify complete`. The work was
on `main` with the engine's record of the passing run. What the runs do not
show: a long interactive session.

### Round 4: `at` builds `at`

The prime-time review's findings were fixed through `atask` on this
repository: a plan (`docs/plans/prime-time.yaml`) of 20 tasks under one
epic, worked by parallel coding agents and by hand, each task with a
proof that failed before the change and passed after, gated by the
repository's own regression (gofmt, go vet, go test). Every task
completed through `atask verify complete`. Later fixes (merge
identity, run logs, first-contact CLI, onboarding friction, these docs)
went the same way. What it found in `at` itself:

- A doubled `GOFLAGS=-count=1` turned the project's own suite red under
  `at` (found by `atask doctor`, fixed first).
- Workers were charged failures for a cancelled database write; a
  cancelled write is now `CANCELLED`, never a strike.
- Merges were committed as a fixed `atask <at@localhost>`; they
  now use the repository's identity and carry `At-Task:` trailers.

### Fresh install

A new agent with no context installed `at` with `go install
…/cmd/atask@main` (through the default module proxy, first try), built a
small Go project, and drove two tasks to completion from the README
alone, including a deliberate failure and a release and re-claim with the
handoff. It found no
blocker. Its first-contact friction was fixed: `atask --help` exited 2,
`atask version` said `dev`, the tour did not say to commit the files `atask init` writes,
and `atask prune` left `.tmp` directories. Documented rather than
changed: `DONE`/`STALLED` exit 1 and failed attempts' commits stay in
history.

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
| `ErrDone` vs `ErrStalled` | `TestWaitingClaimDoneAndStalled`                                                                                                                                                                                   |
| Empty checks              | `TestEmptyChecksFailClosed`                                                                                                                                                                                        |
| Provisional checks        | `TestProvisionalChecksRunFreshAndNeverComplete`                                                                                                                                                                    |
| No evidence reuse         | `TestNoEvidenceReuseAndSameSnapshot`, `TestGoTestCacheDisabledInCheckEnvironment`                                                                                                                                  |
| Same snapshot             | `TestNoEvidenceReuseAndSameSnapshot`, `workspace.TestTaskWorktreesAndSnapshots`                                                                                                                                    |
| Parallel isolation        | not implemented: suites run sequentially on one snapshot (see "Unsupported")                                                                                                                                       |
| Final success             | `TestFinalSuccessIsAtomicAndStacksDependents`                                                                                                                                                                      |
| Final failure             | `TestFinalFailureKeepsClaimAndBlocksDownstream`                                                                                                                                                                    |
| Late blocker              | `TestLateBlockerPreventsCompletion`                                                                                                                                                                                |
| Completed-edge mutation   | `TestCompletedContractsCannotChangeSilently`                                                                                                                                                                       |
| Worker crash/restart      | `TestVerifierCrashRecovery`, `TestTokenFencingAcrossExpiryAndSupersession`                                                                                                                                         |
| Idempotent completion ack | `TestFinalSuccessIsAtomicAndStacksDependents` (replay section)                                                                                                                                                     |
| CLI rendering             | `cli.TestListGolden`, `cli.TestShowGolden`, `cli.TestAckAndVerifyRendering` (`internal/cli/testdata/*.txt`), `e2e.TestAgentLoopEndToEnd`                                                                           |
| CLI security              | `e2e.TestAgentLoopEndToEnd` (no tokens in read views; renew/release reject missing and stale tokens outside a worktree and need none inside it; a token for another task is refused in a worktree; JSON parseable) |

## Invariants (handoff section 2)

| # | Invariant                                                                                                 | Status                                                                                                                                                                       |
| - | --------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1 | Task-ID reads never convey authority                                                                      | holds: every mutation needs a token or planner authority                                                                                                                     |
| 2 | Token writes only while current, unexpired, not ended, not superseded; validated in the write transaction | holds: `execution.Authorize` inside each `Write`                                                                                                                             |
| 3 | No completion with unfinished requires, missing proofs, stale version, or mismatched revision             | holds: `executeFinal` finalisation transaction                                                                                                                               |
| 4 | Completion and claim end are one transaction                                                              | holds                                                                                                                                                                        |
| 5 | Released claim is available again unless blocked or in cooldown                                           | holds: `TestReleaseAndCooldown`, `TestDiscoveredBlockerNeedsSessionOrPlanner`                                                                                                |
| 6 | Parent/child never becomes a hard edge                                                                    | holds: `TestGroupsAreOrganizationalOnly`                                                                                                                                     |
| 7 | Edits to claimed/completed contracts cannot keep old evidence                                             | holds: completed tasks reject edits; claimed-task contract edits need the planner and fail a running verification closed (`TestContractChangeDuringVerificationFailsClosed`) |

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
- **Single host.** SQLite on a local filesystem.
- **Platforms.** Linux is tested (CI and the pilots); macOS and Windows
  cross-compile but are not exercised.
