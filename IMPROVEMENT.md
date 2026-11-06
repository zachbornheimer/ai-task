# Improvement: `atask claim` returns, and leaves a session `verify` resolves from the claiming directory

## Intent

"Make `atask claim` return and leave a resolvable session so `atask verify`
works from the claiming directory." A claim of `at-6ht8sd` from the
`homelab` repo root recorded `ATTEMPT #1`, then never exited and printed
nothing; stdout captured through a pipe, a file or a pty was empty; a second
claim answered `CONFLICT ... the session token was printed only at claim
time`; `atask verify complete` answered `session token required`. The same
hang was running in two other sessions at the time.

## Observations

- `claimOnce` (`internal/app/claim.go`) kept the token of a Git claim only in
  memory until its last step: it recorded the attempt, built the worktree
  (`Manager.EnsureTask` -> `wt switch`), and only then wrote the token into
  the worktree (`workspace.StoreToken`). Only a rootless claim stored a session
  keyed by the claiming directory (`storeRootlessToken`), after its work.
- Nothing bounded the worktree build. The CLI context has no deadline;
  `WorktrunkSwitcher.Ensure` ran `wt` through `process.Command` (plain
  `exec.CommandContext` + `CombinedOutput`): on cancel only `wt` is killed,
  and `CombinedOutput` then waits for every holder of its output pipe. `wt
  switch` runs `git worktree add` and the repository's hooks (the homelab
  worktrunk config has a `copy-ignored` step); a descendant that outlives `wt`
  holds the pipe, so `Ensure` does not return (reproduced: 20.3s past a 300ms
  context). `CopyIgnored` also ran with the worktree lock held and ignored its
  context, so every other claim in the repository queued behind it, silently.
- The five-minute lease in the observed output is `execution.ClaimWindow`: the
  attempt was recorded with it and nothing ever finished or released it.
- `atask help` already says the token resolves from "the directory you
  claimed from"; for a Git project that was never true.
- `process.Run` already had the fix for its own commands (process group +
  `WaitDelay`); `wt` did not use it.

## Contract

- A claim stores its session under the claiming directory (state dir beside
  the database, keyed by directory and task) in the step after it records the
  attempt and before any slow step, for rootless and Git projects.
  `claimOnce` -> `storeClaimSession` (`internal/app/claim_session.go`).
  Resolution order is unchanged: argument, `ATASK_SESSION`, the token in the
  task worktree, then the directory's session.
- Building the workspace is bounded by the engine's claim budget
  (`Config.ClaimBudget`, default `execution.ClaimWindow`, five minutes). A
  claim that is not done by then, or is interrupted, ends what it started,
  gives the task back (`EndReleased`), removes its session and fails with
  `NOT_READY` saying so (`failClaim`, `giveBackClaim`). Any other failure still
  marks the task `needs_attention`, as before.
- `process.Contained` is the one constructor for a command that runs
  descendants of its own: cancel ends its whole process group and `Wait`
  returns within five seconds regardless. `process.Run` and `wt` use it.
- While a claim waits on the worktree lock, `wt switch` or the dependency
  copy, `warning: still ...` lines go to stderr every 15 seconds (naming the
  lock holder's pid). Stdout stays the result; `warning:` is the prefix the
  e2e suite already allows on a successful command.
- Claiming the task again from the directory whose claim never got a
  workspace takes it over (new attempt supersedes the stuck one) instead of
  `CONFLICT`. Claiming again after a built claim returns that claim, as
  rootless already did.
- `CopyIgnored` runs outside the worktree lock and honours its context.
- Docs: `docs/contract.md` (Claim), `docs/invariants.md`, `docs/limitations.md`.

## Proof

Red (before the fix, `-count=1`, machine load ~500):

```text
--- FAIL: TestClaimLeavesItsSessionWhereVerifyFindsIt
    the claiming directory resolves token "" (<nil>), want the claim's "sess-..."
--- FAIL: TestClaimStoresItsSessionBeforeBuildingTheWorktree
    while the worktree was being built the directory resolved "" (<nil>), want the claim's token
--- FAIL: TestClaimThatCannotBuildItsWorktreeReturnsAndReleasesTheTask
    the claim was still blocked after 10s although its claim window is 300ms
--- FAIL: TestKilledClaimIsTakenOverFromItsDirectory (then named ...IsResumed...)
    CONFLICT: at-dkwa39 is claimed by attempt 1 until ...; the session token was printed only at claim time.
--- FAIL: TestWorktrunkSwitcherEnsureEndsWithItsContext
    Ensure returned after 20.347864625s although its context ended after 300ms
--- FAIL: TestClaimFromRepoRootLeavesASessionTheDirectoryResolves
    log from the claiming directory: 2 usage error: session token required: run inside the task worktree or the directory you claimed from, ...
```

Green: the same tests pass (`internal/app`, `internal/workspace`, `internal/cli`),
plus `TestSlowWorktreeBuildSaysWhatItWaitsOn`, `TestWorktreeLockWaitNamesTheHolder`.

## Not proven

- Which step of the observed `at-6ht8sd` claim blocked is inferred from the
  mechanism above and the `copy-ignored` step in the homelab worktrunk config;
  the hung processes were not attached to.
- `atask prune` leaving completed tasks' worktrees and `.tmp` / `.stale-N`
  directories is not changed. Reading `Manager.Prune` (it finds the live
  worktree by task branch, else guesses `<workspace root>/<id>`, while
  Worktrunk places worktrees at `<repo>/../.worktrees/<repo>-<branch>`) gives a
  lead, not a diagnosis. The fix does not depend on a workspace prune can
  remove: the session lives in the state dir, not in the workspace.
- The suite is load-sensitive. On the unmodified base, with the machine's load
  average at ~500, `TestSpentBudgetChecksSayNotRun`, `TestBudgetsTimeOutWithoutAStrike`
  and `TestWaitingClaimDoneAndStalled` (wall-clock budgets of 2-5s) fail the
  same way as on this branch.
