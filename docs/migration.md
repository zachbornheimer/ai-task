# Migration: `tasks` → `at`

The engine was renamed and its contract revised to the "Agent Task (`at`)"
handoff. Databases created by the earlier `tasks` binary migrate in place
(migrations 0002 and 0003 are additive; legacy `task-…` IDs still parse).

## Environment and files

| Before | After |
|---|---|
| binary `tasks` (`cmd/tasks`) | binary `at` (`cmd/at`) |
| `TASKS_DB`, `TASKS_PROJECT`, `TASKS_OUTPUT`, `TASKS_SESSION` | `AT_DB`, `AT_PROJECT`, `AT_OUTPUT`, `AT_SESSION` |
| `~/.local/state/tasks/tasks.db` | `~/.local/state/at/at.db` (worktrees under `…/at/workspaces/`) |
| task IDs `task-` + 16 chars | `at-` + 6 chars (legacy IDs accepted) |

## CLI

| Before | After | Notes |
|---|---|---|
| `tasks add "t" --requires A --blocks B` | `at add "t" --key K --requires A --blocks B` | keys are stable references; `--parent`, `--group`, `--cohort` added; `--manual` removed |
| `tasks deps add B --requires A` | `at update B --requires A` | `--remove-requires`, `--set-requires` |
| `tasks deps remove B --requires A` | `at update B --remove-requires A` | |
| `tasks deps list B` / `tasks graph` | `at show B` (REQUIRES / BLOCKS sections), `at list` | no graph command |
| `tasks take [ID] [--wait D]` | `at claim [REF] [--wait]` | wait returns `DONE` / `STALLED` instead of timing out |
| `tasks renew-task-lease <token>` | `at claim renew <token\|->` | token is mandatory |
| `tasks release <token>` | `at claim release <token\|-> [--failed]` | |
| `tasks log <token> …` | `at log …` (AT_SESSION, `--session`, positional or `-`) | |
| `tasks finish <token>` | `at verify complete` | records submission, runs BOTH suites fresh, integrates, completes |
| `tasks verify <task> [--retry] [--again] [--no-reuse]` | `at verify task` / `at verify regression` (diagnostic) | no evidence reuse exists any more; a stuck run is closed by reconciliation and the task re-claimed |
| `tasks policy <task> --check …` | `at update <task> --check …` | planner authority; rejected on claimed/completed tasks without `--planner` |
| `tasks evidence <task>` | `at show <task> --full` | |
| `tasks status` | `at status` | `done`/`stalled` plus reasons and pending cohorts |
| `tasks list --available/--takeable/--blocked` | `at list ready` / `at list blocked` / `at list all` | glyph hierarchy |
| `tasks project --regression-json` | `at project --regression-json` or `--regression-check` | also `--target-branch`, `--max-attempts`, `--retry-cooldown` |

Error codes renamed: `TASK_ALREADY_TAKEN`→`ALREADY_CLAIMED`,
`NO_AVAILABLE_TASK`→`NO_ELIGIBLE_WORK`, `TASK_AWAITING_VERIFICATION`→
`AWAITING_VERIFICATION`, `TASK_AWAITING_INTEGRATION`→`INTEGRATION_PENDING`.
New: `PLAN_CONFLICT`, `DUPLICATE_KEY`, `MISSING_VERIFICATION`,
`REVISION_CHANGED`, `INTEGRATION_FAILED`, `DONE`, `STALLED`.

## Go API

| Before | After |
|---|---|
| `tasks.Open` → `*Engine` (concrete) | `at.Open` → `*at.Store`, which implements the `at.Engine` interface |
| `Add`, `AddWithDependencies`, `AddDependency`, `RemoveDependency`, `SetTaskPolicy` | `Apply(ctx, ChangeSet)` with `AddGroup` / `AddTask` / `UpdateTask` / `ArchiveTask` |
| `Take(ctx, TakeRequest)` | `Claim(ctx, ClaimRequest{Wait})`; `ErrDone`, `ErrStalled` |
| `Finish(ctx, token, opts)` | `Verify(ctx, token, VerifyComplete)` |
| `Verify(ctx, taskID, opts)` | `Verify(ctx, token, VerifyTask \| VerifyRegression)` |
| `Release(ctx, token, note)` | `Release(ctx, token, ReleaseOptions{Note, Failed})` |
| `Renew(ctx, token, lease)` | `Renew(ctx, token)` (default lease) |
| `List(ctx, pid, ListFilter)`, `Available`, `Takeable`, `Graph` | `List(ctx, ListQuery{Filter})` → `PlanSnapshot` |
| `Show(ctx, id)`, `History`, `Evidence` | `Show(ctx, pid, ref, full)` |
| `Summary` | `Summary` (adds `PendingCohorts`, `Reasons`) |

## Semantics that changed

- A task with no task checks, or a project with no regression checks, can
  no longer complete: `verify complete` fails closed with
  `MISSING_VERIFICATION`. Previously an empty policy completed vacuously.
- Every verification executes its checks. The evidence-reuse cache is gone.
- Each attempt gets its own Git worktree (`at/<task>/<n>`); `verify
  complete` snapshots the committed revision and runs there; promotion to
  the target branch is a guarded compare-and-swap, so Git projects default
  to integration policy `promote`.
- Completion re-checks prerequisites, contract revision, regression policy,
  and the run's own evidence in the finalising transaction.
- Prerequisites cannot be added to completed tasks; completed tasks cannot
  be edited or archived.
- Human-gated (`--manual`) tasks were removed: a task with no runnable
  checks cannot complete, so a human attestation path would be needed and
  is not provided in V1.
