# Verification

Verification is separate from implementation. An agent's word never
completes a task; only `verify complete` can, and only with fresh evidence.

## Two required proofs

- **Proof** (`atask add --proof`, `atask update --proof`) proves the task's
  own outcome. Required unless a task is a group.
- **Regression** (`atask add --regression`, `atask update --regression`)
  proves nothing else broke. It belongs to the task; a project holds no
  checks (`atask project` sets only target branch and attempts).

A task missing either proof is a draft and cannot be claimed or completed.

Each proof is `"PRESET: SUBJECT [key=value ...]"`, run in the task worktree.
Every check runs under its own `size=` budget (30/300/900 s, or none for
`eternal`). `docs/contract.md` lists the presets.

## Modes

| Invocation                | Inputs                                              | Effect                                                                        |
| ------------------------- | --------------------------------------------------- | ----------------------------------------------------------------------------- |
| `atask verify proof`      | attempt worktree (editable)                         | runs the proof fresh; records a diagnostic run; never completes               |
| `atask verify regression` | attempt worktree                                    | same for the regression                                                       |
| `atask verify complete`   | clean worktree → immutable HEAD → detached snapshot | runs BOTH proofs fresh, finalises completion, then runs `on_complete` actions |
| `atask verify`            |                                                     | usage error                                                                   |

The regression is skipped (recorded as `skipped`) when the proof failed.
Only `passed` counts.

## No reuse

Every invocation executes its proofs. Stored evidence is history for
diagnosis (`atask show --full`), never proof for a new run. Proofs run with
`GOFLAGS=-count=1` so Go's own test cache cannot replay a success, and
without `ATASK_SESSION` so a proof cannot act on the task. The one exception
is an idempotent acknowledgement: `verify complete` with the same token
after a committed completion returns the stored result and runs nothing.

## Completion algorithm

1. Refuse unless both proofs exist and every prerequisite is complete;
   require a clean worktree and take HEAD as the submission.
2. Record the submission and a running run; renew the lease on a
   heartbeat while proofs run.
3. Run the proof on a detached snapshot of the revision, then the
   regression on a second, fresh snapshot (skipped, and recorded as such,
   when the proof failed): nothing the proof leaves behind can be why the
   regression passes. Each snapshot has a private `TMPDIR`; `ATASK_SESSION`
   is never in a proof's environment. Each evidence row commits as it
   finishes, fenced on the session and on the run still being the newest.
4. Judge. On failure: run failed, failure count recorded, claim stays
   live, `CHECK_FAILED` with evidence.
5. **Complete.** One short transaction re-checks the session,
   prerequisites, the task's contract revision, archive state, submission
   identity, and that stored evidence proves the candidate; then marks the
   task complete, records its revision, ends the claim and records its
   `on_complete` actions as pending.
6. **Actions.** atask runs each action. Waited actions record a
   `RunResult`; detached actions are spawned and recorded as `detached`.
   Completion is already recorded: a failed action never undoes it, and
   `verify complete` on the completed task retries only pending or failed
   actions.

Completion never moves the target branch. Dependents start from the target
branch plus a merge of every required task's completed revision in this
repository. A requirement completed in another repository is sequencing
only and is not merged. Putting work on the target branch is an
`on_complete` action (for example `run: wt merge main`).

No SQLite transaction is ever held across a Git command or a proof process.

## Check TMPDIR

Checks and actions run with a `TMPDIR` private to the directory they run
in, so one suite's temporary files never reach another. It is
`/tmp/atask-<uid>/<16 hex characters>`, the hex being a SHA-256 prefix of
the directory's path, never a path beside the worktree: its length does not
depend on the worktree's, so a unix socket made inside it (OpenTofu's
provider plugin socket, `$TMPDIR/plugin<10 digits>`) stays under the
104-byte `sun_path` limit of macOS however deep the worktree is.
`ATASK_CHECK_TMP_ROOT` replaces `/tmp/atask-<uid>`; the root must be a
directory only its owner can enter, or checks get the system temp dir
instead. The directory is removed with its snapshot or worktree (`atask
prune`, snapshot cleanup).

## Groups

A group's proofs run on the merge of every child's completed revision, built in a scratch worktree; the group completes only after every child is complete and no integration branch exists. `verify complete GROUP` fails with `NOT_READY` until then, and with `BASE_CONFLICT` when the children cannot be merged.

## Budgets: proofs must be fast

Every check declares a size with `size=small|medium|large|eternal` (30/300/900 s,
or no budget); a proof is small by default, a regression takes its preset's size. A run over its budget is killed and
fails with `timed_out=true`. A timeout says the verification was built too
slow for the task (or the code hangs), not that the software is wrong. A
`go_test` on a whole tree with no `name` is rejected at add/update as a
small proof, because a package suite routinely outruns the budget.

## Run results

Every executed proof runs through `proof.Runner` and records one
`RunResult` (`check_type`, `preset`, `subject`, `options`, `started_at`,
`elapsed_ms`, `budget_ms`, `timed_out`, `return_code`, `passed`, `output`
tail) in the `run_results` table, keyed by task, attempt and run. A run
over its budget is killed (whole process group) and fails with
`timed_out=true` and `return_code=null`. `atask show --full` prints one line
per result (and returns them as `run_results`); `verify proof|regression|
complete --json` returns the `run_results` of that run.
