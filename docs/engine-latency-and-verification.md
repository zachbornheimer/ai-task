# Engine latency and verification reliability

Findings of a read-only diagnosis of the atask engine against a remote
PostgreSQL board (at ffb5d4b), and what changed in response. Measurements
are from that diagnosis; anything not measured is marked.

## What was wrong

1. **Every CLI call took 7 s or more, and `list complete` took minutes.**
   The board is remote, so each statement is a network round trip
   (100 to 195 ms measured with `select 1`). Three runs of `atask show`
   took 6.8, 7.0 and 7.6 s with 0.1 s of CPU, which implies roughly 60
   sequential statements (derived from latency, not counted).
   - `Open` ran `migrate` on every call: a `CREATE TABLE IF NOT EXISTS`
     plus a transaction.
   - Most commands called `reconcile()` first: a write transaction of five
     or six `UPDATE`s and a `SELECT`, even on a read.
   - `buildViewDepth` queried per row (parent, check history,
     requirements). `list complete` on about 2,800 tasks multiplied that.
2. **A verification run stayed "in progress" with no way out.** The reaper
   existed (owner pid gone, or two missed heartbeats), but a process that
   outlived its caller's timeout kept stamping heartbeats and looked alive.
   190 `execution_attempts` rows had `ended_at IS NULL`, and 189 of them had
   expired leases.
3. **Heavy suites timed out.** A check with no `size=` gets the 30 s small
   budget, and each suite runs in a fresh snapshot, so a warm 55 to 60 s
   cargo suite became a cold build.
4. **Minutes passed between the last check finishing and the run closing.**
   The DB shows 1 to 71 s of recording; time after the commit (prune,
   actions, snapshot removal) was not visible anywhere.
5. **Run logs were found by directory name.** Run 2806 existed under two
   project ids; only one belongs to this board.

## What changed

| Area        | Change                                                                                                                                                                                                                                                                                                                          | Test                                                                                |
| ----------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| Listing     | Set-based loading of a listing's relations (cherry-picked from `2b4ad90`, which also bounds PostgreSQL statements and idle transactions)                                                                                                                                                                                        | `TestListQueryCountIsConstant`                                                      |
| Open        | `schema_migrations` is read first; the migrate write runs only when a version is missing. A version recorded under another name still reaches the existing mismatch error                                                                                                                                                       | `TestOpenSkipsMigrationWriteWhenCurrent`                                            |
| Reads       | `reconcile` asks a read-only probe (`Tx.ReconcilePending`, sharing its predicates with `ReconcileRuns`) and opens a write transaction only when something is pending. It was not removed from `List` and `Show`, because the reap is observed through them                                                                      | `TestReadsOfAnIdleBoardIssueNoWrites`, `TestOrphanedVerificationRunIsReaped`        |
| Attempts    | `ReconcileRuns` ends attempts whose lease expired unclaimed (`end_reason = expired`). An expired attempt keeps the meaning of a lapsed open one: the task still derives `interrupted`, the next claim still quarantines its worktree, its token gets the lease-expired message, and `--reset-attempts` turns it into `released` | `TestReconcileEndsExpiredAttempts`, `TestExpiredAttemptKeepsTheMeaningOfALapsedOne` |
| Cancel      | `atask verify cancel [TOKEN]` closes the session's running runs as `abandoned:` (no attempt charged). A closed run can no longer complete, and its lingering process stops without rewriting the run's record, so it stays `abandoned:` and charges no failure                                                                  | `TestVerifyCancelClosesLiveRun`, `TestCanceledRunCannotComplete`                    |
| Signals     | SIGTERM and SIGHUP stop a CLI verification and then close its run on a fresh, bounded context                                                                                                                                                                                                                                   | `TestTerminationSignalAbandonsTheRun`, `TestAbandonVerifyClosesTheRunAsTerminated`  |
| Build cache | `CARGO_TARGET_DIR`, `CARGO_HOME`, `RUSTUP_HOME`, `SCCACHE_DIR`, `RUSTC_WRAPPER` reach checks. Exact names: the `CARGO_REGISTRY_TOKEN` family stays blocked                                                                                                                                                                      | `TestSnapshotChecksSeeBuildCacheEnv`                                                |
| Budgets     | `add` and `update` warn when a `run:` check looks like a full build or suite and sets no `size=`                                                                                                                                                                                                                                | `TestHeavyWithoutSize`, `TestAddWarnsAboutAHeavyCheckWithoutSize`                   |
| Timings     | The `verify complete` result carries `phases` (`snapshot_ms`, `checks_ms`, `cleanup_ms`, `record_ms`, `prune_ms`, `actions_ms`)                                                                                                                                                                                                 | `TestVerifyResultReportsPhaseTimings`                                               |
| History     | `list complete`, `list archived`, and the completed part of the default `atask list` return the newest 200 (`--limit N`, `--limit 0` for all) and say how many they omitted; open work is never limited                                                                                                                         | `TestListCompleteIsBoundedByDefault`                                                |
| Logs        | `atask run show ID` resolves the run's log directory through the board                                                                                                                                                                                                                                                          | `TestRunLogDirResolvesThroughStore`                                                 |
| Help        | `--outcome` is documented as the long spec field shown by `atask context ID`                                                                                                                                                                                                                                                    | `TestOutcomeFlagIsDocumentedAsTheSpecField`                                         |

## Not done, and why

- **Batching the final completion transaction.** It is already one
  transaction (`fence`, recheck, `FinishRun`, `MarkComplete`, `EndAttempt`).
  The remaining per-check cost is the separate fence and evidence write after
  each check. `phases.checks_ms` includes it, so the next measurement on the
  remote board shows whether it is worth changing.
- **Pruning in the background.** A goroutine dies when the CLI exits, so
  pruning would silently never run. It stays synchronous and is reported as
  `phases.prune_ms`. A detached process would be the way to move it, and
  should wait for that number.
- **Killing a lingering verifier.** `verify cancel` closes the run; the old
  process stops at its next fence, but a check already executing runs until
  its own budget.
- **Sharing a build cache automatically.** Passing the user's cache
  variables through is done; creating a shared cache per project is not.

## Rollout

Upgrade every binary that writes to a shared board together. A binary that
predates `expired` reads a reaped attempt as cleanly ended: the task shows
as `ready`, and its claim reuses the stale worktree instead of quarantining
it. `agent-fleet-backend` writes to the same board.

## Caveats

- The PostgreSQL paths (the reconcile probe, the migrate fast path,
  `CancelRunningRuns`, the `ResetAttempts` update) are covered by
  `TestReconcileScenarioOnPostgreSQL`, which runs when
  `ATASK_TEST_POSTGRES_URL` names a database (it works in a schema of its
  own and drops it). The default suite skips it. They were also exercised
  end to end through the CLI on a local PostgreSQL 16: two idle reads logged
  12 data-changing statements with `ffb5d4b` and none with this branch.
  Not run against the production board.
- Latency was not re-measured against the remote board after these changes;
  the tests count statements on SQLite through a wrapped driver
  (`internal/sqlite/stmtcount`), which is what scales with round trips.
- Runs from `agent-fleet-backend` share the board; their owner pids cannot be
  probed from another host, so only the heartbeat rule or `verify cancel`
  closes them.
- The diagnosis could not reproduce the 15-minute "in progress" run from the
  database: run 2806 is a clean 307 s timeout. The cancel and signal paths
  address the plausible cause (a surviving process), which is unverified.
