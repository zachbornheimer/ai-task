# Migration: `tasks` → `at`

The engine was renamed and its contract revised to the "Agent Task (`at`)"
handoff. Databases created by the earlier `tasks` binary migrate in place
(migrations 0002 and 0003 are additive; legacy `task-…` IDs still parse).

## Environment and files

| Before                                                       | After                                                          |
| ------------------------------------------------------------ | -------------------------------------------------------------- |
| binary `tasks` (`cmd/tasks`)                                 | binary `atask` (`cmd/atask`)                                   |
| `TASKS_DB`, `TASKS_PROJECT`, `TASKS_OUTPUT`, `TASKS_SESSION` | `ATASK_DB`, `ATASK_PROJECT`, `ATASK_OUTPUT`, `ATASK_SESSION`   |
| `~/.local/state/tasks/tasks.db`                              | `~/.local/state/at/at.db` (worktrees under `…/at/workspaces/`) |
| task IDs `task-` + 16 chars                                  | `at-` + 6 chars (legacy IDs accepted)                          |

## CLI

| Before                                                 | After                                                                              | Notes                                                                                                |
| ------------------------------------------------------ | ---------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| `tasks add "t" --requires A --blocks B`                | `atask add "t" --key K --requires A --blocks B`                                    | keys are stable references; `--parent`, `--group` added; `--manual` removed                          |
| `tasks deps add B --requires A`                        | `atask update B --requires A`                                                      | `--remove-requires` (no `--set-requires`: removed)                                                   |
| `tasks deps remove B --requires A`                     | `atask update B --remove-requires A`                                               |                                                                                                      |
| `tasks deps list B` / `tasks graph`                    | `atask show B` (REQUIRES / BLOCKS sections), `atask list`                          | no graph command                                                                                     |
| `tasks take [ID] [--wait D]`                           | `atask claim [REF] [--wait]`                                                       | wait returns `DONE` / `STALLED` instead of timing out                                                |
| `tasks renew-task-lease <token>`                       | `atask claim renew [token\|-]`                                                     | token from the argument, `-`, the task worktree or ATASK_SESSION                                     |
| `tasks release <token>`                                | `atask claim release [token\|-] [--failed]`                                        |                                                                                                      |
| `tasks log <token> …`                                  | `atask log …` (token from the task worktree or ATASK_SESSION; `--session` removed) |                                                                                                      |
| `tasks finish <token>`                                 | `atask verify complete`                                                            | records submission, runs BOTH proofs fresh, completes                                                |
| `tasks verify <task> [--retry] [--again] [--no-reuse]` | `atask verify proof` / `atask verify regression` (diagnostic)                      | no evidence reuse exists any more; a stuck run is closed by reconciliation and the task re-claimed   |
| `tasks policy <task> --check …`                        | `atask update <task> --proof … --regression …`                                     | planner authority; rejected on claimed/completed tasks with the task's own token (`--check` removed) |
| `tasks evidence <task>`                                | `atask show <task> --full`                                                         |                                                                                                      |
| `tasks status`                                         | `atask status`                                                                     | `done`/`stalled` plus reasons                                                                        |
| `tasks list --available/--takeable/--blocked`          | `atask list ready` / `atask list blocked` / `atask list all`                       | glyph hierarchy                                                                                      |
| `tasks project --regression-json`                      | `atask project`                                                                    | `--target-branch`, `--attempts` only (`--retry-cooldown` removed); proofs live on tasks              |

Error codes renamed: `TASK_ALREADY_TAKEN`→`ALREADY_CLAIMED`,
`NO_AVAILABLE_TASK`→`NO_ELIGIBLE_WORK`, `TASK_AWAITING_VERIFICATION`→
`AWAITING_VERIFICATION`, `TASK_AWAITING_INTEGRATION` is gone.
Current codes are listed in `docs/contract.md`.

## Go API

| Before                                                                             | After                                                                              |
| ---------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| `tasks.Open` → `*Engine` (concrete)                                                | `at.Open` → `*at.Store`, which implements the `at.Engine` interface                |
| `Add`, `AddWithDependencies`, `AddDependency`, `RemoveDependency`, `SetTaskPolicy` | `Apply(ctx, ChangeSet)` with `AddGroup` / `AddTask` / `UpdateTask` / `ArchiveTask` |
| `Take(ctx, TakeRequest)`                                                           | `Claim(ctx, ClaimRequest{Wait})`; `ErrDone`, `ErrStalled`                          |
| `Finish(ctx, token, opts)`                                                         | `Verify(ctx, VerifyRequest{TaskID, Token, Mode: VerifyComplete})`                  |
| `Verify(ctx, taskID, opts)`                                                        | `Verify(ctx, VerifyRequest{TaskID, Token, Mode: VerifyProof \| VerifyRegression})` |
| `Release(ctx, token, note)`                                                        | `Release(ctx, ReleaseRequest{Token, Note, Failed})`                                |
| `Renew(ctx, token, lease)`                                                         | `Renew(ctx, token)` (default lease)                                                |
| `List(ctx, pid, ListFilter)`, `Available`, `Takeable`, `Graph`                     | `List(ctx, ListQuery{Filter})` → `PlanSnapshot`                                    |
| `Show(ctx, id)`, `History`, `Evidence`                                             | `Show(ctx, id)`                                                                    |
| `Summary`                                                                          | `Summary` (adds `Reasons`)                                                         |

## Semantics that changed

- A task without both proofs is a draft: never claimable, never completed.
  Previously an empty policy completed vacuously.
- Every verification executes its proofs. The evidence-reuse cache is gone.
- Every project is a Git repository (`atask init` creates one; `--no-dir` is
  gone). Each task gets one worktree on branch `atask/<task-id>`, reused by
  every attempt; `verify complete` snapshots the committed revision and
  runs there. Completion records the revision and never moves the target
  branch.
- Leases are a fixed 30 minutes; every authenticated command renews them.
  The token is never stored on disk; hosts inject it as `ATASK_SESSION`.
- `--archive` needs `--reason`.
- JSON output is always one compact line (`--pretty` removed); `help` and a
  bare `at` print envelopes in JSON mode.
- `--reset-attempts` is planner authority (no task token); `--group` and
  `--archive` refuse unrelated flags.
- `atask prune` removes worktrees of complete/archived tasks and quarantined
  directories; completion removes a task's own quarantines.
- Completion re-checks prerequisites, contract revision, and the run's own
  evidence in the finalising transaction.
- Prerequisites cannot be added to completed tasks; completed tasks cannot
  be edited or archived.
- Human-gated (`--manual`) tasks were removed: a task with no runnable
  proofs cannot complete, so a human attestation path would be needed and
  is not provided in V1.

## Moving a promote project to an on_complete action

A project that used `--integration promote` now completes without moving the
target branch. Cohorts, pins, `--integration`, the integration intent and the
`awaiting_integration` status are gone; groups replace cohorts (a group's proofs
run on the merge of every child's completed revision).

To keep work landing on `main`, give each task (or plan-file item) an
`on_complete` action:

```sh
atask add "Fix the typo" --proof "run: go test ./x" --regression "ci" \
  --on-complete "run: wt merge main"
# or, without worktrunk:
#   --on-complete "run: git -C /path/to/repo merge --ff-only atask/<id>"
```

Completion is recorded first. A failed action never undoes it; `atask verify
complete` on the completed task retries only pending or failed actions. The
repo the action touches is the user's. An existing database keeps a stored
`promote` value in its `projects.integration` column; it is ignored.
